package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grafov/m3u8"
)

type Config struct {
	Models  []ModelInfo `json:"models"`
	SaveDir string      `json:"save_dir"`
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
	StartDownload(SaveDir string)
	DownloadPartFile(PartUrl string, ExtXMap string) bool
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
	StopChanEvent          chan bool
	CurrentSaveFilePath    string // current save file path
}

func NewTask(config Config, modelName string, taskMap map[string]*Task) *Task {
	return &Task{
		Config:                 config,
		ModelName:              modelName,
		HasStart:               false,
		SaveDir:                config.SaveDir,
		CurrentSegmentSequence: 0,
		StreamName:             "",
		OnlineM3u8File:         "",
		ExtXMap:                "",
		PartToDownload:         []string{},
		PartDownFinished:       []string{},
		TaskMap:                taskMap,
		StopChanEvent:          make(chan bool),
		CurrentSaveFilePath:    "",
	}
}

func (t *Task) IsOnline() (bool, string) {
	if t.ModelName == "" {
		log.Println("ModelName is empty")
		return false, ""
	}
	CamInfoUri := fmt.Sprintf("https://stripchat.com/api/front/v2/models/username/%s/cam", t.ModelName)
	resp, err := http.Get(CamInfoUri)
	if err != nil {
		log.Println("Get cam info failed, error:", err)
		return false, ""
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Println("Get cam info failed, status code:", resp.StatusCode)
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

func (t *Task) GetPlayList() {
	if t.OnlineM3u8File == "" {
		log.Println("OnlineM3u8File is empty")
		return
	}
	resp, err := http.Get(t.OnlineM3u8File)
	if err != nil || resp.StatusCode != 200 {
		log.Println("Get m3u8 file failed, error:", err)
		return
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("Read m3u8 file failed, error:", err)
		return
	}
	// log.Printf("body %s:", string(body))
	b := bytes.NewReader(body)
	playlist, _, err := m3u8.DecodeFrom(b, false)
	if err != nil {
		// panic(err)
		log.Panicf("(%s) Decode m3u8 file failed, error: %s", t.ModelName, err)
		return
	}
	mediaPlaylist, ok := playlist.(*m3u8.MediaPlaylist)
	if !ok {
		log.Printf("(%s) MediaPlaylist is empty", t.ModelName)
		return
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
					log.Panicf("ExtXMap is not equal, %s, %s", t.ExtXMap, segment.Map.URI)
				}
				// part file is not exist in PartToDownload and PartDownFinished,add to PartToDownload
				if !Contains(t.PartToDownload, segment.URI) && !Contains(t.PartDownFinished, segment.URI) {
					t.PartToDownload = append(t.PartToDownload, segment.URI)
					log.Printf("(%s) add new segment: %s", t.ModelName, segment.URI)
					return
				}
			}
		}
		t.CurrentSegmentSequence = int(mediaPlaylist.SeqNo)
	}

}

func (t *Task) DownloadPartFile(PartUrl string, ExtXMap string) bool {
	// 1. down part file
	resp, err := http.Get(PartUrl)
	if err != nil {
		log.Printf("(%s) Download part file failed, error: %s. uri %s", t.ModelName, err, PartUrl)
		return false
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Printf("(%s) Download part file failed, error: %s. uri %s", t.ModelName, err, PartUrl)
			return false
		} else {
			file, _ := os.OpenFile(t.CurrentSaveFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
			defer file.Close()
			data, _ := ioutil.ReadAll(resp.Body)
			file.Write(data)
			log.Printf("(%s) Download part file success, uri %s", t.ModelName, PartUrl)
		}
	}
	return true
}

func (t *Task) StartDownload() {
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
	file, _ := os.OpenFile(t.CurrentSaveFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	defer file.Close()

	// download init file first
	log.Printf("(%s) is Online . start task,begin downloading init file", t.ModelName)
	// fileName := strings.Split(t.ExtXMap, "/")[len(strings.Split(t.ExtXMap, "/"))-1]
	resp, err := http.Get(t.ExtXMap)
	if err != nil {
		log.Printf("(%s) Download init file failed, error: %s. uri %s", t.ModelName, err, t.ExtXMap)
		return
	} else {
		data, _ := ioutil.ReadAll(resp.Body)
		file.Write(data)
		for {
			if len(t.PartToDownload) == 0 {
				time.Sleep(5 * time.Second)
				continue
			} else if !t.HasStart {
				log.Printf("(%s) Task downloader stop", t.ModelName)
				return
			} else {
				partUri := t.PartToDownload[0]
				t.DownloadPartFile(partUri, t.ExtXMap)
				t.PartToDownload = t.PartToDownload[1:]
				t.PartDownFinished = append(t.PartDownFinished, partUri)
			}
		}
	}

}

func (t *Task) Run() {
	defer close(t.StopChanEvent)
	t.HasStart = true
	t.GetPlayList()
	go t.StartDownload()
	for {
		select {
		case <-t.StopChanEvent:
			log.Printf("(%s) Task begin stop", t.ModelName)
			t.HasStart = false
			return
		default:
			if ok, _ := t.IsOnline(); !ok {
				log.Printf("(%s) Model is offline", t.ModelName)
				t.HasStart = false
				return
			} else {
				t.GetPlayList()
			}
		}
	}

}

func main() {
	os.Setenv("HTTP_PROXY", "http://127.0.0.1:10809")
	os.Setenv("HTTPS_PROXY", "http://127.0.0.1:10809")
	taskMap := make(map[string]*Task)

	for {
		configFile := "./config.json"
		config := Config{}
		file, err := ioutil.ReadFile(configFile)
		if err != nil {
			log.Fatalf("Read config file failed, error: %s", err)
		}
		err = json.Unmarshal(file, &config)
		if err != nil {
			log.Fatalf("Unmarshal config file failed, error: %s", err)
		}

		for _, model := range config.Models {
			task := NewTask(config, model.Name, taskMap)
			if ok, _ := task.IsOnline(); !ok {
				log.Printf("Model %s is offline", model.Name)
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
		log.Println("reload config file after 20s ....")
		time.Sleep(20 * time.Second)

	}
}
