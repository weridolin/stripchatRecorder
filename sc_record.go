package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/smtp"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grafov/m3u8"
	"github.com/jordan-wright/email"
)

// get_psch_pkey_from_m3u8 从m3u8内容中提取PSCH和PKEY
func getPschKeyFromM3u8(m3u8Content string) (string, string) {
	lines := strings.Split(m3u8Content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-MOUFLON:PSCH:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 4 {
				return parts[2], parts[3]
			}
		}
	}
	return "", ""
}

// get_decrypt_key 获取解密密钥
func getDecryptKey(pkey string) (string, error) {
	// 创建HTTP客户端
	client := &http.Client{}

	// 发送请求获取静态配置
	req, err := http.NewRequest("GET", "https://hu.stripchat.com/api/front/v3/config/static", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	// 设置User-Agent头
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应内容
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	// 解析JSON响应
	var staticResp map[string]interface{}
	if err := json.Unmarshal(body, &staticResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	// 提取static数据
	staticData, ok := staticResp["static"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("static field not found or invalid type")
	}

	// 提取features
	features, ok := staticData["features"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("features field not found or invalid type")
	}

	// 提取MMPExternalSourceOrigin
	mmpOrigin, ok := features["MMPExternalSourceOrigin"].(string)
	if !ok {
		return "", fmt.Errorf("MMPExternalSourceOrigin field not found or invalid type")
	}

	// 提取featuresV2
	featuresV2, ok := staticData["featuresV2"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("featuresV2 field not found or invalid type")
	}

	// 提取playerModuleExternalLoading
	playerModule, ok := featuresV2["playerModuleExternalLoading"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("playerModuleExternalLoading field not found or invalid type")
	}

	// 提取mmpVersion
	mmpVersion, ok := playerModule["mmpVersion"].(string)
	if !ok {
		return "", fmt.Errorf("mmpVersion field not found or invalid type")
	}

	// 构建MMP base URL
	mmpBase := fmt.Sprintf("%s/v%s", mmpOrigin, mmpVersion)

	// 请求main.js
	mainJSReq, err := http.NewRequest("GET", fmt.Sprintf("%s/main.js", mmpBase), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create main.js request: %v", err)
	}
	mainJSReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3")

	mainJSResp, err := client.Do(mainJSReq)
	if err != nil {
		return "", fmt.Errorf("failed to get main.js: %v", err)
	}
	defer mainJSResp.Body.Close()

	mainJSBody, err := io.ReadAll(mainJSResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read main.js: %v", err)
	}

	mainJS := string(mainJSBody)

	// 使用正则表达式查找Doppio*.js文件
	re := regexp.MustCompile(`require\(\"\.\/(Doppio.*?\.js)\"\)`)
	matches := re.FindStringSubmatch(mainJS)
	if len(matches) < 2 {
		return "", fmt.Errorf("doppio.js not found in main.js")
	}

	doppioJS := matches[1]

	// 请求Doppio*.js文件
	doppioJSReq, err := http.NewRequest("GET", fmt.Sprintf("%s/%s", mmpBase, doppioJS), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create doppio.js request: %v", err)
	}
	doppioJSReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3")

	doppioJSResp, err := client.Do(doppioJSReq)
	if err != nil {
		return "", fmt.Errorf("failed to get doppio.js: %v", err)
	}
	defer doppioJSResp.Body.Close()

	doppioJSBody, err := io.ReadAll(doppioJSResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read doppio.js: %v", err)
	}

	doppioJSContent := string(doppioJSBody)

	// 使用正则表达式查找解密密钥
	keyRe := regexp.MustCompile(fmt.Sprintf(`"%s:(.*?)"`, regexp.QuoteMeta(pkey)))
	keyMatches := keyRe.FindStringSubmatch(doppioJSContent)
	if len(keyMatches) < 2 {
		return "", fmt.Errorf("decrypt key not found for pkey: %s", pkey)
	}

	return keyMatches[1], nil
}

// decode 使用SHA256密钥解密base64数据
func decode(encryptedB64 string, key string) (string, error) {
	// 计算SHA256哈希值
	hashBytes := sha256.Sum256([]byte(key))
	// 正确处理base64填充
	// base64字符串长度必须是4的倍数，根据需要添加填充
	padding := len(encryptedB64) % 4
	if padding != 0 {
		encryptedB64 += strings.Repeat("=", 4-padding)
	}

	// 解码base64数据
	encryptedData, err := base64.StdEncoding.DecodeString(encryptedB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %v", err)
	}

	// 解密数据
	var decryptedBytes []byte
	for i, cipherByte := range encryptedData {
		keyByte := hashBytes[i%len(hashBytes)]
		decryptedByte := cipherByte ^ keyByte
		decryptedBytes = append(decryptedBytes, decryptedByte)
	}

	// 转换为字符串
	plaintext := string(decryptedBytes)
	return plaintext, nil
}

// extract_mouflon_and_parts 从m3u8内容中提取MOUFLON和PART (修改返回类型以匹配Python版本)
func extractMouflonAndParts(m3u8Content string) [][]string {
	lines := strings.Split(strings.TrimSpace(m3u8Content), "\n")
	var result [][]string

	var mouflonValue string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-MOUFLON:FILE:") {
			// 提取MOUFLON内容，从第三个冒号开始取
			parts := strings.SplitN(line, ":", 3)
			if len(parts) >= 3 {
				mouflonValue = parts[2]
			}
		} else if strings.HasPrefix(line, "#EXT-X-PART:") && mouflonValue != "" {
			// 提取PART URI
			re := regexp.MustCompile(`URI="([^"]+)"`)
			match := re.FindStringSubmatch(line)
			if len(match) >= 2 {
				partURI := match[1]
				result = append(result, []string{mouflonValue, partURI})
			}
			mouflonValue = "" // 重置，保证一一对应
		}
	}

	return result
}

// extract_variant_playlists 从master m3u8中提取变体播放列表
func extractVariantPlaylists(m3u8Content string) map[string]string {
	lines := strings.Split(strings.TrimSpace(m3u8Content), "\n")
	result := make(map[string]string)
	var currentName string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			// 提取NAME="480p"这种标记
			re := regexp.MustCompile(`NAME="([^"]+)"`)
			match := re.FindStringSubmatch(line)
			if len(match) >= 2 {
				currentName = match[1]
			}
		} else if line != "" && !strings.HasPrefix(line, "#") {
			// 遇到URL，和上一个NAME绑定
			if currentName != "" {
				result[currentName] = line
				currentName = ""
			}
		}
	}

	return result
}

/*******  MAP ********/
type TDataMap sync.Map

func (m *TDataMap) Load(key string) (value interface{}, ok bool) {
	return (*sync.Map)(m).Load(key)
}

func (m *TDataMap) Store(key string, value interface{}) {
	(*sync.Map)(m).Store(key, value)
}

func (m *TDataMap) Length() int {
	len := 0
	(*sync.Map)(m).Range(func(_, _ interface{}) bool {
		len++
		return true
	})
	return len
}

func (m *TDataMap) Delete(key string) {
	(*sync.Map)(m).Delete(key)
}

func (m *TDataMap) Range(f func(key, value interface{}) bool) {
	(*sync.Map)(m).Range(f)
}

func (m *TDataMap) GetMaxKey() int {
	maxKey := 0
	(*sync.Map)(m).Range(func(k, _ interface{}) bool {
		if kInt, _ := strconv.Atoi(k.(string)); kInt > maxKey {
			maxKey = kInt
		}
		return true
	})
	return maxKey
}

func (m *TDataMap) GetMinKey() int {
	minKey := m.GetMaxKey()
	(*sync.Map)(m).Range(func(k, _ interface{}) bool {
		if kInt, _ := strconv.Atoi(k.(string)); kInt < minKey {
			minKey = kInt
		}
		return true
	})
	return minKey
}

func GetSyncMapLen(m *sync.Map) int {
	len := 0
	m.Range(func(_, _ interface{}) bool {
		len++
		return true
	})
	return len
}

func GetMaxKey(m *sync.Map) int {
	maxKey := 0
	m.Range(func(k, _ interface{}) bool {
		if kInt, _ := strconv.Atoi(k.(string)); kInt > maxKey {
			maxKey = kInt
		}
		return true
	})
	return maxKey
}

func GetMinKey(m *sync.Map) int {
	minKey := GetMaxKey(m)
	m.Range(func(k, _ interface{}) bool {
		if kInt, _ := strconv.Atoi(k.(string)); kInt < minKey {
			minKey = kInt
		}
		return true
	})
	return minKey
}

type Config struct {
	Models  []ModelInfo `json:"models"`
	SaveDir string      `json:"save_dir"`
	Proxy   Proxy       `json:"proxy"`
	Notify  Notify      `json:"notify"`
}

type Notify struct {
	Enable   bool   `json:"enable"`
	Smtp     string `json:"smtp"`
	Port     int    `json:"port"`
	PassWord string `json:"password"`
	Sender   string `json:"sender"`
	Receiver string `json:"receiver"`
}

type Proxy struct {
	Enable bool   `json:"enable"`
	Uri    string `json:"uri"`
}

type ModelInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type viewServers struct {
	Flashphoner    string `json:"flashphoner"`
	FlashphonerVr  string `json:"flashphoner-vr"`
	FlashphonerHls string `json:"flashphoner-hls"`
}

type CamInfoBrief struct {
	// only get info we need
	StreamName     string      `json:"streamName"`
	IsCamAvailable bool        `json:"isCamAvailable"`
	ViewServers    viewServers `json:"viewServers"`
}

type GetCamInfoRespBrief struct {
	Cam CamInfoBrief `json:"cam"`
}

type DownloaderImpl interface {
	IsOnline() (bool, string)
	GetPlayList()
	Downloader(SaveDir string)
	DownloadPartFile(PartUrl string, ExtXMap string) bool
	FileWriter()
	Run()
}

func Contains(s []string, e string) bool {
	if err := recover(); err != nil {
		log.Println("Contains panic:", err)
		return false
	}
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

type Task struct {
	Config                 Config
	ModelName              string
	HasStart               bool
	SaveDir                string
	CurrentSegmentSequence int
	StreamName             string
	OnlineM3u8File         string
	ExtXMap                string
	PartToDownload         TDataMap
	PartDownFinished       TDataMap
	TaskMap                map[string]*Task
	CurrentSaveFilePath    string // current save file path
	NotifyMessageChan      chan<- NotifyMessage
	// NewLiveStreamEvent     chan bool
	IsDownloaderStart bool
	IsFileWriterStart bool
	DataMap           TDataMap
	ListOpLock        sync.Mutex
	DecodeKeyPairs    map[string]string
	Pkey              string
	mediaUri          string
}

func NewTask(config Config, modelName string, taskMap map[string]*Task, notifyMessageChan chan<- NotifyMessage) *Task {
	return &Task{
		Config:                 config,
		ModelName:              modelName,
		HasStart:               false,
		SaveDir:                config.SaveDir,
		CurrentSegmentSequence: -1,
		StreamName:             "",
		OnlineM3u8File:         "",
		ExtXMap:                "",
		PartToDownload:         TDataMap{},
		PartDownFinished:       TDataMap{},
		TaskMap:                taskMap,
		CurrentSaveFilePath:    "",
		NotifyMessageChan:      notifyMessageChan,
		IsDownloaderStart:      false,
		IsFileWriterStart:      false,
		DataMap:                TDataMap{},
		ListOpLock:             sync.Mutex{},
		DecodeKeyPairs:         make(map[string]string), // 初始化解密密钥映射
		Pkey:                   "",                      // 初始化Pkey
		mediaUri:               "",
	}
}

func (t *Task) init() {
	t.HasStart = false
	t.CurrentSegmentSequence = -1
	t.StreamName = ""
	t.OnlineM3u8File = ""
	t.ExtXMap = ""
	t.PartToDownload = TDataMap{}
	t.PartDownFinished = TDataMap{}
	t.CurrentSaveFilePath = ""
	t.IsDownloaderStart = false
	t.IsFileWriterStart = false
	t.SaveDir = ""
}

func (t *Task) IsOnline() (bool, string) {
	if t.ModelName == "" {
		log.Println("ModelName is empty")
		return false, ""
	}

	CamInfoUri := fmt.Sprintf("https://stripchat.com/api/front/v2/models/username/%s/cam", t.ModelName)
	resp, err := http.Get(CamInfoUri)
	if err != nil {
		log.Printf("(%s) Get cam info failed, error: %s", t.ModelName, err)
		return false, ""
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("(%s) Get cam info failed,status code:%v, error: %s", t.ModelName, resp.StatusCode, err)
		return false, ""
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("(%s) Read cam info failed, error: %s", t.ModelName, err)
		return false, ""
	}

	var camInfo GetCamInfoRespBrief
	err = json.Unmarshal(body, &camInfo)
	if err != nil {
		log.Printf("(%s) Unmarshal cam info failed, error: %s", t.ModelName, err)
		return false, ""
	}

	log.Printf("(%s) cam info: %+v", t.ModelName, camInfo)

	if !camInfo.Cam.IsCamAvailable || camInfo.Cam.StreamName == "" {
		log.Printf("(%s) model is offline or stream name is empty", t.ModelName)
		return false, ""
	}

	// 参考 Python 版本，使用新的 m3u8 URL 格式
	// online_mu3u8_uri = f'https://edge-hls.doppiocdn.com//hls/{resp["cam"]["streamName"]}/master/{resp["cam"]["streamName"]}_auto.m3u8'
	m3u8File := fmt.Sprintf("https://edge-hls.doppiocdn.com//hls/%s/master/%s_auto.m3u8", camInfo.Cam.StreamName, camInfo.Cam.StreamName)
	log.Printf("(%s) is online, m3u8 file: %s", t.ModelName, m3u8File)

	t.OnlineM3u8File = m3u8File
	t.StreamName = camInfo.Cam.StreamName
	return true, m3u8File
}

// _getSequence 从URL中提取序列号 (对应Python的_get_sequence方法)
func (t *Task) _getSequence(partUrl string) string {
	re := regexp.MustCompile(`_(\d+)_`)
	match := re.FindStringSubmatch(partUrl)
	if len(match) > 1 {
		return match[1]
	}
	return ""

}

func (t *Task) getMediaUri() string {
	if t.OnlineM3u8File == "" {
		log.Printf("(%s) OnlineM3u8File is empty", t.ModelName)
		fmt.Errorf("OnlineM3u8File is empty")
	}

	// 创建HTTP客户端
	client := &http.Client{}

	// 第一步：获取master m3u8文件
	req, err := http.NewRequest("GET", t.OnlineM3u8File, nil)
	if err != nil {
		fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3")

	// log.Printf("(%s) get m3u8 file -> %s", t.ModelName, t.OnlineM3u8File)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		log.Printf("(%s) Get m3u8 file failed, error: %s", t.ModelName, err)
		fmt.Errorf("get m3u8 file failed, error: %s", err)
	}
	defer resp.Body.Close()

	masterM3u8Body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("(%s) Read m3u8 file failed, error: %s", t.ModelName, err)
		fmt.Errorf("read m3u8 file failed, error: %s", err)
	}

	masterM3u8Content := string(masterM3u8Body)

	// 第二步：从master m3u8中提取pkey和psch
	psch, pkey := getPschKeyFromM3u8(masterM3u8Content)
	if psch == "" {
		log.Printf("(%s) get psch from m3u8 file failed", t.ModelName)
		fmt.Errorf("get psch from m3u8 file failed")
	}
	if pkey == "" {
		log.Printf("(%s) get pkey from m3u8 file failed", t.ModelName)
		fmt.Errorf("get pkey from m3u8 file failed")
	}

	t.Pkey = pkey
	// 第三步：提取变体播放列表
	variantPlaylists := extractVariantPlaylists(masterM3u8Content)
	var mediaUri string
	if uri, exists := variantPlaylists["480p"]; exists {
		mediaUri = uri
	} else if uri, exists := variantPlaylists["source"]; exists {
		mediaUri = uri
	}

	if mediaUri == "" {
		log.Printf("(%s) get media uri from m3u8 file failed", t.ModelName)
		fmt.Errorf("get media uri from m3u8 file failed")
	}

	// 第四步：获取解密密钥
	var decryptKey string
	if existingKey, exists := t.DecodeKeyPairs[pkey]; exists {
		decryptKey = existingKey
	} else {
		key, err := getDecryptKey(pkey)
		if err != nil {
			log.Printf("(%s) get decrypt key failed, error: %s", t.ModelName, err)
			fmt.Errorf("get decrypt key failed: %v", err)
		}
		decryptKey = key
		t.DecodeKeyPairs[pkey] = decryptKey
	}

	// log.Printf("(%s) get decrypt key: %s from pkey -> %s", t.ModelName, decryptKey, pkey)

	// 第五步：构建带参数的media URI
	mediaUriWithParams := fmt.Sprintf("%s?psch=%s&pkey=%s&playlistType=lowLatency", mediaUri, psch, pkey)
	log.Printf("(%s) get absolute media uri -> %s", t.ModelName, mediaUriWithParams)
	t.mediaUri = mediaUriWithParams
	return mediaUriWithParams
}

func (t *Task) GetPlayList() error {

	// 创建HTTP客户端
	client := &http.Client{}

	mediaReq, err := http.NewRequest("GET", t.mediaUri, nil)
	if err != nil {
		return fmt.Errorf("failed to create media request: %v", err)
	}
	mediaReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3")

	mediaResp, err := client.Do(mediaReq)
	if err != nil {
		log.Printf("(%s) Get media m3u8 failed, error: %s", t.ModelName, err)
	}
	defer mediaResp.Body.Close()

	mediaM3u8Body, err := ioutil.ReadAll(mediaResp.Body)
	if err != nil {
		log.Printf("(%s) Read media m3u8 failed, error: %s", t.ModelName, err)
		return fmt.Errorf("read media m3u8 failed: %v", err)
	}

	mediaM3u8Content := string(mediaM3u8Body)

	b := bytes.NewReader(mediaM3u8Body)
	playlist, _, err := m3u8.DecodeFrom(b, false)
	if err != nil {
		log.Printf("(%s) Decode media m3u8 failed, error: %s %s", t.ModelName, err, mediaM3u8Content)
		return fmt.Errorf("decode media m3u8 failed: %v", err)
	}

	mediaPlaylist, ok := playlist.(*m3u8.MediaPlaylist)
	if !ok {
		log.Printf("(%s) MediaPlaylist is not valid", t.ModelName)
		return fmt.Errorf("MediaPlaylist is not valid")
	}

	// 更新当前段序列号
	t.CurrentSegmentSequence = int(mediaPlaylist.SeqNo)

	// 获取ExtXMap (初始化段)
	if len(mediaPlaylist.Segments) > 0 && mediaPlaylist.Segments[0] != nil && mediaPlaylist.Segments[0].Map != nil {
		t.ExtXMap = mediaPlaylist.Segments[0].Map.URI
	}
	// 提取和处理mouflon和parts
	// fmt.Println("><<<<<", mediaM3u8Content)
	mouflonAndParts := extractMouflonAndParts(mediaM3u8Content)
	// log.Printf("(%s) mouflonAndParts: %v", t.ModelName, mouflonAndParts)
	for _, mouflonPart := range mouflonAndParts {
		if len(mouflonPart) < 2 {
			continue
		}
		mouflon := mouflonPart[0]
		part := mouflonPart[1]

		// 解码mouflon得到真实的part URL
		realPartUrl, err := decode(mouflon, t.DecodeKeyPairs[t.Pkey])
		if err != nil {
			log.Printf("(%s) decode mouflon failed: %v", t.ModelName, err)
			continue
		}

		// 构建完整的part URL
		partDir := part[:strings.LastIndex(part, "/")]
		fullRealPartUrl := fmt.Sprintf("%s/%s", partDir, realPartUrl)

		// 获取序列号
		sequence := t._getSequence(fullRealPartUrl)
		if sequence == "" {
			log.Printf("(%s) get sequence from URL failed: %s", t.ModelName, fullRealPartUrl)
			continue
		}

		// 判断PartToDownload是否存在sequence，不存在就添加列表
		_, inDownload := t.PartToDownload.Load(sequence)
		if !inDownload {
			// 如果同时存在，则跳过
			t.PartToDownload.Store(sequence, []string{})

		}

		// 判断是否存在列表中,不存才添加
		exist := false
		val, _ := t.PartToDownload.Load(sequence)
		urls, _ := val.([]string)
		for _, url := range urls {
			if url == fullRealPartUrl {
				exist = true
				break
			}
		}
		if !exist {
			urls = append(urls, fullRealPartUrl)
			t.PartToDownload.Store(sequence, urls)
			log.Printf("(%s) add part url -> %s to sequence -> %s", t.ModelName, fullRealPartUrl, sequence)
		}
	}
	return nil
}

func (t *Task) FileWriter(ctx context.Context) {
	log.Printf("(%s) task file writer start ...  %s ", t.ModelName, t.CurrentSaveFilePath)
	t.IsFileWriterStart = true
	// wait until t.DataMap is not empty and t.CurrentSegmentSequence is not -1
WAIT:
	if t.CurrentSegmentSequence == -1 {
		time.Sleep(1 * time.Second)
		goto WAIT
	}

	file, err := os.OpenFile(t.CurrentSaveFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		log.Printf("(%s) Failed to open the file: %v\n %s", t.ModelName, err, t.CurrentSaveFilePath)
		return
	}
	defer file.Close()

	// startIndex := t.CurrentSegmentSequence
	for {
		select {
		case <-ctx.Done():
			log.Printf("(%s) task stop write file ... maybe model is offline,begin to write rest data from dataMap to file", t.ModelName)
			// if dataMap is not empty,write data to file
			minKey := t.DataMap.GetMinKey()
			maxKey := t.DataMap.GetMaxKey()
			for i := minKey; i <= maxKey; i++ {
				// get data from map by sequence,start from t.CurrentSegmentSequence,if not exist,wait 2min max,else continue
				key := fmt.Sprintf("%d", i)
				if dataList, ok := t.DataMap.Load(key); ok {
					dataList, _ := dataList.([][]byte)
					for _, data := range dataList {
						file.Write(data)
					}
					log.Printf("(%s) write data to file %s, sequence -> %s", t.ModelName, t.CurrentSaveFilePath, key)
					t.DataMap.Delete(key)
				}
				// file.Close()
			}
			t.IsFileWriterStart = false
			return
		default:
			minKey := t.DataMap.GetMinKey()
			maxKey := t.DataMap.GetMaxKey()
			// fmt.Println("minKey ->", minKey, "maxKey ->", maxKey, t.DataMap.Length())
			for i := minKey; i <= maxKey; i++ {
				// get data from map by sequence,start from t.CurrentSegmentSequence,if not exist,wait 2min max,else continue
				key := fmt.Sprintf("%d", i)
				if dataList, ok := t.DataMap.Load(key); ok {
					dataList, _ := dataList.([][]byte)
					for _, data := range dataList {
						file.Write(data)
					}
					log.Printf("(%s) write data to file %s, sequence -> %s", t.ModelName, t.CurrentSaveFilePath, key)
					t.DataMap.Delete(key)
				}
				// file.Close()
			}
			t.IsFileWriterStart = true
			// time.Sleep(1 * time.Second)
		}
	}

}

func (t *Task) DownloadPartFile(PartUrl []string, ExtXMap string, Sequence string) bool {
	defer func() {
		if err := recover(); err != nil {
			log.Printf("(%s) Download part file failed, error: %s. uri %s", t.ModelName, err, PartUrl)
			return
		}
	}()
	// 1. down part file
	log.Printf("(%s) Download part file list, sequence -> %s, uri count: %d", t.ModelName, Sequence, len(PartUrl))
	for _, url := range PartUrl {
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("(%s) Download part file failed, error: %s. uri %s", t.ModelName, err, url)
			return false
		} else {
			defer resp.Body.Close()
			data, _ := ioutil.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				log.Printf("(%s) Download part file failed, error: %s. uri %s statusCode:%v \n response %s", t.ModelName, err, url, resp.StatusCode, data)
				return false
			} else {
				// 检查DataMap中是否已存在该Sequence
				if dataList, ok := t.DataMap.Load(Sequence); ok {
					// 将 data 添加到 dataList 中
					dataList = append(dataList.([][]byte), data)
					t.DataMap.Store(Sequence, dataList)
				} else {
					// 第一次存储该序列号
					t.DataMap.Store(Sequence, [][]byte{data})
				}
				// log.Printf("(%s) Download part file success, uri %s,sequence -> %s ", t.ModelName, url, Sequence)
			}

		}
	}
	log.Printf("(%s) Download part file list success, uri %s,sequence -> %s", t.ModelName, PartUrl, Sequence)
	return true
}

func (t *Task) Downloader(ctx context.Context) {
	defer func() {
		log.Printf("(%s) task downloader stop feed path -> %s ", t.ModelName, t.CurrentSaveFilePath)
		t.IsDownloaderStart = false
	}()
	if t.IsDownloaderStart {
		log.Printf("(%s) task downloader is already start", t.ModelName)
		return
	} else {
		log.Printf("(%s) task downloader start", t.ModelName)
		t.IsDownloaderStart = true
	}
	// RESTART:
	// create save dir if not exist
	if t.Config.SaveDir == "" {
		log.Printf("(%s) SaveDir is empty", t.ModelName)
		return
	}
	// get live stream name from ExtXMap
	fileName := strings.Split(t.ExtXMap, "/")[len(strings.Split(t.ExtXMap, "/"))-1]
	timeStr := time.Now().Format("2006-01-02")
	t.SaveDir = filepath.Join(t.Config.SaveDir, t.ModelName, timeStr)
	if err := os.MkdirAll(t.SaveDir, os.ModePerm); err != nil {
		log.Printf("(%s) Failed to create the dir: %v\n", t.ModelName, err)
		return
	}

	// current live file save path
	t.CurrentSaveFilePath = filepath.Join(t.SaveDir, fileName)
	_, err := os.Stat(t.CurrentSaveFilePath)
	if err != nil && os.IsNotExist(err) {
		file, createErr := os.Create(t.CurrentSaveFilePath)
		if createErr != nil {
			log.Printf("(%s) Failed to create the file: %v\n", t.ModelName, createErr)
		}
		file.Close()
	}

	// download init file first
	log.Printf("(%s) is Online . start task,begin downloading init file", t.ModelName)
	resp, err := http.Get(t.ExtXMap)

	if err != nil {
		log.Printf("(%s) Download init file failed, error: %s. uri %s", t.ModelName, err, t.ExtXMap)
		return
	}
	defer resp.Body.Close()

	data, _ := ioutil.ReadAll(resp.Body)
	file, _ := os.OpenFile(t.CurrentSaveFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	file.Write(data)
	file.Close() // loop , don't user defer

	for {
		select {
		case <-ctx.Done():
			log.Printf("(%s) task stop download ... maybe model is offline", t.ModelName)
			t.NotifyMessageChan <- NotifyMessage{
				ModelName: t.ModelName,
				Message:   "model live stream down finish",
				SavePath:  t.CurrentSaveFilePath,
				Type:      "down_finish",
			}
			return
		default:
			t.ListOpLock.Lock()
			minKey := t.PartToDownload.GetMinKey()
			maxKey := t.PartToDownload.GetMaxKey()
			for i := minKey; i < maxKey; i++ {
				sequence := fmt.Sprintf("%d", i)
				if partUriList, exists := t.PartToDownload.Load(sequence); exists {
					t.DownloadPartFile(partUriList.([]string), t.ExtXMap, sequence)
					//从PartToDownload中删除该sequence
					t.PartToDownload.Delete(sequence)
				}
			}
			t.ListOpLock.Unlock()
		}
	}

}

func (t *Task) Run() {
	log.Printf("(%s) task start running ....", t.ModelName)
	defer func() {
		log.Printf("(%s) task stop...", t.ModelName)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	t.HasStart = true
	go t.FileWriter(ctx)
	t.getMediaUri()
	for {
		err := t.GetPlayList()
		if err != nil {
			// if error ，restart task
			log.Printf("(%s) GetPlayList failed, error: %s", t.ModelName, err)
			cancel()
			// t.TaskMap[t.ModelName] = nil
			delete(t.TaskMap, t.ModelName)
			return
		}
		if !t.IsDownloaderStart {
			go t.Downloader(ctx)
		}
	}
	// }

}

/********************      email notify          *****************/
type NotifyMessage struct {
	ModelName string `json:"model_name"`
	Message   string `json:"message"`
	SavePath  string `json:"save_path"`
	Type      string `json:"type"`
}
type NotifierImpl interface {
	run(config Config, msgChan <-chan NotifyMessage)
}

type EmailNotifier struct {
	Notify
	HasStart bool
}

func (e *EmailNotifier) run(ctx context.Context, msgChan <-chan NotifyMessage) {
	if e.HasStart {
		log.Println("EmailNotifier is already start")
		return
	}
	e.HasStart = true
	select {
	case msg := <-msgChan:
		log.Printf("EmailNotifier get message: %s", msg.Message)
		// e.SendEmail(msg)
	case <-ctx.Done():
		e.HasStart = false
		log.Println("EmailNotifier stop...")
		return
	default:

	}
}

func (e *EmailNotifier) SendEmail(msg NotifyMessage) {
	switch msg.Type {
	case "down_finish":
		content := fmt.Sprintf("Model %s live stream download finish, save path: %s", msg.ModelName, msg.SavePath)
		obj := email.NewEmail()
		//设置发送方的邮箱
		obj.From = fmt.Sprintf("st-recorder <%s>", e.Sender)
		// 设置接收方的邮箱
		obj.To = []string{e.Receiver}
		//设置主题
		obj.Subject = "Model live stream download finish"
		//设置文件发送的内容
		obj.HTML = []byte(content)
		//设置服务器相关的配置
		err := obj.Send(fmt.Sprintf("%s:%v", e.Smtp, e.Port), smtp.PlainAuth("", e.Sender, e.PassWord, e.Smtp))
		if err != nil {
			log.Println("send email failed ->", err)
		}
		return
	}
}

func NewEmailNotifier(config Config) *EmailNotifier {
	return &EmailNotifier{
		Notify: Notify{
			Enable:   config.Notify.Enable,
			Smtp:     config.Notify.Smtp,
			Sender:   config.Notify.Sender,
			PassWord: config.Notify.PassWord,
			Port:     config.Notify.Port,
		},
		HasStart: false,
	}
}

func LoadConfig(configFile string) Config {
	config := Config{}
	file, err := ioutil.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Read config file failed, error: %s", err)
	}
	err = json.Unmarshal(file, &config)
	if err != nil {
		log.Fatalf("Unmarshal config file failed, error: %s", err)
	}
	return config
}

func main() {
	// go func() {
	// 	log.Println(http.ListenAndServe(":6060", nil))
	// }()

	config := LoadConfig("config.json")

	// os.Setenv("HTTP_PROXY", config.Proxy.Uri)
	// os.Setenv("HTTPS_PROXY", config.Proxy.Uri)
	// CamInfoUri := "https://stripchat.com/api/front/v2/models/username/selina530/cam"
	// resp, err := http.Get(CamInfoUri)
	// if err != nil {
	// 	// log.Println("()Get cam info failed, error:", err)
	// 	log.Printf(" Get cam info failed, error: %s", err)
	// 	return

	// } else {
	// 	fmt.Println("resp:", resp, resp.StatusCode)
	// 	defer resp.Body.Close()
	// }
	// return

	notifyCtx, notifyCancel := context.WithCancel(context.Background())
	taskMap := make(map[string]*Task)
	notifyMessageChan := make(chan NotifyMessage)
	defer close(notifyMessageChan)
	emailNotifier := NewEmailNotifier(config)
	for {
		config := LoadConfig("config.json")
		if config.Notify.Enable {
			emailNotifier.PassWord = config.Notify.PassWord
			emailNotifier.Port = config.Notify.Port
			emailNotifier.Smtp = config.Notify.Smtp
			emailNotifier.Sender = config.Notify.Sender
			if !emailNotifier.HasStart {
				go emailNotifier.run(notifyCtx, notifyMessageChan)
			}
		} else {
			notifyCancel()
		}
		if config.Proxy.Enable {
			log.Printf("Set proxy uri: %s", config.Proxy.Uri)
			os.Setenv("HTTP_PROXY", config.Proxy.Uri)
			os.Setenv("HTTPS_PROXY", config.Proxy.Uri)
		} else {
			os.Unsetenv("HTTP_PROXY")
			os.Unsetenv("HTTPS_PROXY")
		}

		for _, model := range config.Models {
			task := NewTask(config, model.Name, taskMap, notifyMessageChan)
			if ok, _ := task.IsOnline(); !ok {
				log.Printf("Model %s is offline", model.Name)
				delete(taskMap, model.Name)
				continue
			} else {
				if _, ok := taskMap[model.Name]; ok {
					log.Printf("Model %s is already in taskMap", model.Name)
					continue
				}
				taskMap[model.Name] = task
				go task.Run()
			}

		}
		log.Printf("reload config file after 20s ... taskMap: %v", taskMap)
		time.Sleep(20 * time.Second)
	}
}
