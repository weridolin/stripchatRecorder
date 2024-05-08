package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	PartToDownload         []string
	PartDownFinished       []string
	TaskMap                map[string]*Task
	CurrentSaveFilePath    string // current save file path
	NotifyMessageChan      chan<- NotifyMessage
	// NewLiveStreamEvent     chan bool
	IsDownloaderStart bool
	IsFileWriterStart bool
	DataMap           sync.Map
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
		PartToDownload:         []string{},
		PartDownFinished:       []string{},
		TaskMap:                taskMap,
		CurrentSaveFilePath:    "",
		NotifyMessageChan:      notifyMessageChan,
		// NewLiveStreamEvent: make(chan bool),
		IsDownloaderStart: false,
		IsFileWriterStart: false,
		DataMap:           sync.Map{},
	}
}

func GetPartSequence(PartUrl string) string {
	re := regexp.MustCompile(`_(\d+)_`)
	match := re.FindStringSubmatch(PartUrl)
	if len(match) > 0 {
		return match[1]
	} else {
		return ""
	}
}

func (t *Task) init() {
	t.HasStart = false
	t.CurrentSegmentSequence = -1
	t.StreamName = ""
	t.OnlineM3u8File = ""
	t.ExtXMap = ""
	t.PartToDownload = []string{}
	t.PartDownFinished = []string{}
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
		// log.Println("()Get cam info failed, error:", err)
		log.Printf("(%s) Get cam info failed, error: %s", t.ModelName, err)
		return false, ""
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("(%s) Get cam info failed,status code:%v, error: %s", t.ModelName, resp.StatusCode, err)
		return false, ""
	} else {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Println("Read cam info failed, error:", err)
			return false, ""
		}
		var camInfo GetCamInfoRespBrief
		err = json.Unmarshal(body, &camInfo)
		if err != nil {
			log.Println("Unmarshal cam info failed, error:", err)
			return false, ""
		}
		if !camInfo.Cam.IsCamAvailable || camInfo.Cam.StreamName == "" {
			return false, ""
		}
		// /f'https://b-{resp["cam"]["viewServers"]["flashphoner-hls"]}.doppiocdn.com/hls/{resp["cam"]["streamName"]}/{resp["cam"]["streamName"]}.m3u8'
		m3u8File := fmt.Sprintf("https://b-%s.doppiocdn.com/hls/%s/%s.m3u8", camInfo.Cam.ViewServers.FlashphonerHls, camInfo.Cam.StreamName, camInfo.Cam.StreamName)
		log.Printf("model %s is  online,Get m3u8 file: %s", t.ModelName, m3u8File)
		t.OnlineM3u8File = m3u8File
		return true, m3u8File
	}
}

func (t *Task) GetPlayList() error {

	if t.OnlineM3u8File == "" {
		log.Println("OnlineM3u8File is empty")
		return fmt.Errorf("OnlineM3u8File is empty")
	}
	resp, err := http.Get(t.OnlineM3u8File)
	if err != nil || resp.StatusCode != 200 {
		// if stream is over,get 404
		log.Println("Get m3u8 file failed, error:", err)
		return fmt.Errorf(fmt.Sprintf("Get m3u8 file failed, error: %s", err))
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("Read m3u8 file failed, error:", err)
		return fmt.Errorf(fmt.Sprintf("Read m3u8 file failed, error: %s", err))
	}
	resp.Body.Close()
	b := bytes.NewReader(body)
	playlist, _, err := m3u8.DecodeFrom(b, false)
	if err != nil {
		// panic(err)
		log.Panicf("(%s) Decode m3u8 file failed, error: %s", t.ModelName, err)
		return fmt.Errorf(fmt.Sprintf("Decode m3u8 file failed, error: %s", err))
	}
	mediaPlaylist, ok := playlist.(*m3u8.MediaPlaylist)
	if !ok {
		log.Printf("(%s) MediaPlaylist is empty", t.ModelName)
		return fmt.Errorf("MediaPlaylist is empty")
	}

	// check CurrentSegmentSequence is larger CurrentSegmentSequence
	if int(mediaPlaylist.SeqNo) > t.CurrentSegmentSequence {
		// segment may be nil in mediaPlaylist.Segments
		for _, segment := range mediaPlaylist.Segments {
			if segment != nil {
				if t.ExtXMap == "" && segment.Map != nil {
					// ONLY FIRST SEGMENT HAS EXT-X-MAP ?
					t.ExtXMap = segment.Map.URI
				} else if segment.Map != nil && t.ExtXMap != segment.Map.URI {
					log.Printf("ExtXMap is not equal, %s, %s stop current live stream downloading and start new one", t.ExtXMap, segment.Map.URI)
					// return fmt.Errorf("ExtXMap is not equal")
				}
				// part file is not exist in PartToDownload and PartDownFinished,add to PartToDownload
				if !Contains(t.PartToDownload, segment.URI) && !Contains(t.PartDownFinished, segment.URI) {
					t.PartToDownload = append(t.PartToDownload, segment.URI)
					log.Printf("(%s) add new segment to PartToDownload list: %s", t.ModelName, segment.URI)
					continue
				}
			}
		}
		t.CurrentSegmentSequence = int(mediaPlaylist.SeqNo)
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

	startIndex := t.CurrentSegmentSequence
	for {
		select {
		case <-ctx.Done():
			log.Printf("(%s) task stop write file ... maybe model is offline,begin to write rest data from dataMap to file", t.ModelName)
			// if dataMap is not empty,write data to file
			minKey := GetMinKey(&t.DataMap)
			maxKey := GetMaxKey(&t.DataMap)
			for i := minKey; i <= maxKey; i++ {
				// get data from map by sequence,start from t.CurrentSegmentSequence,if not exist,wait 2min max,else continue
				// file, err := os.OpenFile(t.CurrentSaveFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
				// if err != nil {
				// 	log.Printf("(%s) Failed to open the file: %v\n %s", t.ModelName, err, t.CurrentSaveFilePath)
				// 	return
				// }
				key := fmt.Sprintf("%d", i)
				if data, ok := t.DataMap.Load(key); ok {
					log.Printf("(%s) write data to file %s, sequence -> %s", t.ModelName, t.CurrentSaveFilePath, key)
					file.Write(data.([]byte))
					t.DataMap.Delete(key)
				}
				// file.Close()
			}
			t.IsFileWriterStart = false
			return
		default:
			key := fmt.Sprintf("%d", startIndex)
			// fmt.Println("t.DataMap:", key, startIndex, t.CurrentSegmentSequence)
			if data, ok := t.DataMap.Load(key); ok {
				log.Printf("(%s) write data to file %s, sequence -> %s", t.ModelName, t.CurrentSaveFilePath, key)
				_, err = file.Write(data.([]byte))
				if err != nil {
					log.Printf("(%s) write data to file failed, error: %s", t.ModelName, err)
				}
				// delete(t.DataMap, key)
				t.DataMap.Delete(key)
				startIndex++
				// file.Close()
			} else {
				//wait 5s
				time.Sleep(5 * time.Second)
				// if dataMap element count is more than 20,set startIndex to minKey
				if GetSyncMapLen(&t.DataMap) > 20 {
					minKey := GetMinKey(&t.DataMap)
					log.Printf("(%s) dataMap element count is more than 20, set startIndex:%v to minKey %v", t.ModelName, startIndex, minKey)
					startIndex = minKey
				}
			}
		}
	}

}

func (t *Task) DownloadPartFile(PartUrl string, ExtXMap string) bool {
	// 1. down part file
	// log.Printf("(%s) Download part file, uri %s", t.ModelName, PartUrl)
	resp, err := http.Get(PartUrl)
	if err != nil {
		log.Printf("(%s) Download part file failed, error: %s. uri %s", t.ModelName, err, PartUrl)
		return false
	} else {
		defer resp.Body.Close()
		data, _ := ioutil.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			log.Printf("(%s) Download part file failed, error: %s. uri %s statusCode:%v \n response %s", t.ModelName, err, PartUrl, resp.StatusCode, data)
			return false
		} else {
			// get part squenence
			partSequence := GetPartSequence(PartUrl)
			if partSequence == "" {
				log.Printf("(%s) Get part sequence failed, uri %s", t.ModelName, PartUrl)
				return false
			} else {
				// add to map
				log.Printf("(%s) Download part file success, uri %s,sequence -> %s ", t.ModelName, PartUrl, partSequence)
				t.DataMap.Store(partSequence, data)
			}
		}
	}
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
	} else {
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
				if len(t.PartToDownload) == 0 {
					// time.Sleep(2 * time.Second)
					continue
				} else {
					partUri := t.PartToDownload[0]
					// fmt.Println("partUri:", partUri)
					if partUri != "" && t.ExtXMap != "" {
						go t.DownloadPartFile(partUri, t.ExtXMap)
					}
					if len(t.PartToDownload) == 0 {
						continue
					}
					t.PartToDownload = t.PartToDownload[1:]
					t.PartDownFinished = append(t.PartDownFinished, partUri)
					// partDownFinished list only save current 100 records
					if len(t.PartDownFinished) >= 100 {
						t.PartDownFinished = t.PartDownFinished[len(t.PartDownFinished)-100:]
					}
				}
			}
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
	for {
		err := t.GetPlayList()
		if err != nil {
			// if error ，restart task
			log.Printf("(%s) GetPlayList failed, error: %s", t.ModelName, err)
			cancel()
			t.init()
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
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

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
