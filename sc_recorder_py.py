import m3u8
import requests
import aiohttp
import json
import logging
import asyncio
import os
import datetime
import re
import aiofiles

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)


class FlagNotSameError(Exception):
    pass

class TaskFinishError(Exception):
    pass

class ModelOfflineError(Exception):
    def __init__(self,model_name ,*args) -> None:
        self.model_name = model_name
        super().__init__(*args)
    pass

class TaskMixin:

    def __init__(self) -> None:
        self.ext_x_map = None  # 同次直播流对应的唯一标记
        self.online_mu3u8_uri = None ## 直播流地址
        self.current_segment_sequence = 0 # 当前直播流第几个序列片段
        self.stream_name = None # 当前stream name
        self.part_to_down = list() # 等待下载的片段列表 
        self.part_down_finish = list() # 正在下载的片段列表
        self.data_map = {} # 存储下载的数据
        self.current_save_path = None # 保存路径

    async def is_online(self,model_name):
        try:
            async with aiohttp.ClientSession(trust_env=True) as session:
                async with session.get(f'https://stripchat.com/api/front/v2/models/username/{model_name}/cam') as resp:
                    resp = await resp.json()
            with open(f'./{model_name}.json', "w", encoding="utf8") as f:
                json.dump(resp, f, ensure_ascii=False, indent=4)
            m3u8_file = None
            if 'cam' in resp.keys():
                if {'isCamAvailable', 'streamName', 'viewServers'} <= resp['cam'].keys():
                    # streamName is the live mu3u8 stream file name
                    if 'flashphoner-hls' in resp['cam']['viewServers'].keys() and resp['cam']['isCamAvailable'] and resp['cam']['isCamAvailable']:
                        m3u8_file = f'https://b-{resp["cam"]["viewServers"]["flashphoner-hls"]}.doppiocdn.com/hls/{resp["cam"]["streamName"]}/{resp["cam"]["streamName"]}.m3u8'
                        print(f"model -> {model_name} is online, stream m3u8 file -> {m3u8_file}, steam name -> {resp['cam']['streamName']}")
                    else:
                        return False,None
            if m3u8_file:
                return m3u8_file, resp['cam']['streamName']
            return False,None
        except:
            logger.error("Error while checking if model is online", exc_info=True)
            return False,None


    async def get_play_list(self,m3u8_file):
        async with aiohttp.ClientSession() as session:
            async with session.get(m3u8_file) as resp:
                m3u8_obj = m3u8.loads(await resp.text())
                if m3u8_obj.media_sequence > self.current_segment_sequence:
                    ## 有新的片段
                    for segment in m3u8_obj.segments:
                        print(f"({self.model_name}) New segment -> {segment.uri}")
                        if segment.uri not in self.part_to_down and  segment.uri not in self.part_down_finish :
                            self.part_to_down.append(segment.uri)
                        if not self.ext_x_map:
                            self.ext_x_map = segment.init_section.uri
                            # print(f"ext_x_map -> {self.ext_x_map}")
                    self.current_segment_sequence = m3u8_obj.media_sequence
                else:
                    # check if model still online   
                    if not await self.is_online(self.model_name):
                        self.stop_flag = True
                        # self.has_start = False
                        raise ModelOfflineError(self.model_name,f"({self.model_name}) is not online")

                #     print(f"No new segment,  sequence ->  {m3u8_obj.media_sequence}, current sequence -> {self.current_segment_sequence}")
        return m3u8_obj

    
    
    async def download_part_file(self,part_uri):
        sequence = self._get_sequence(part_uri)
        if not sequence:
            print(f"({self.model_name}) Can't get sequence from part uri -> {part_uri}")
            return
        try:
            async with aiohttp.ClientSession() as session:
                async with session.get(part_uri) as resp:
                    if resp.status == 200:
                        print(f"({self.model_name}) Downloading {part_uri} to {self.current_save_path}")
                        # with open(self.current_save_path, "ab") as f:
                        #     f.write(await resp.read())
                        self.data_map[sequence] = await resp.read()
                        print(f"({self.model_name}) Downloading {part_uri} to {self.current_save_path} finish")
                    else:
                        print(f"({self.model_name}) Downloading {part_uri} failed , status code -> {resp.status},response -> {await resp.text()}") 
        except:
            logger.exception("Error while downloading part file", exc_info=True)
        finally:
            self.part_down_finish.remove(part_uri)

    async def _start_downloader(self):
        # if self.part_to_down:
        if not os.path.exists(self.save_dir):
            os.makedirs(self.save_dir)

        if self.ext_x_map:
            ## 下载init文件
            self.current_save_path = os.path.join(self.save_dir,self.ext_x_map.rsplit('/')[-1])
            if not os.path.exists(self.current_save_path):
                async with aiohttp.ClientSession() as session:
                    async with session.get(self.ext_x_map) as resp:
                        if resp.status == 200:
                            print(f"({self.model_name}) Downloading init file {self.ext_x_map} to {self.current_save_path} success...") 
                            with open(self.current_save_path, "ab") as f:
                                f.write(await resp.read())
                        else:
                            print(f"({self.model_name}) Downloading init file {self.ext_x_map} failed , status code -> {resp.status},response -> {await resp.text()}")              
                print(f"({self.model_name}) down load init file finish... begin to down part file...")

        while not self.stop_flag:
            if len(self.part_to_down) == 0:
                print(f"({self.model_name}) No part to download, check again after 5s ...")
                await asyncio.sleep(5)
            else:
                part_uri = self.part_to_down.pop(0)
                loop=asyncio.get_event_loop()
                self.part_down_finish.append(part_uri)
                loop.create_task(self.download_part_file(part_uri))

    async def _start_writer(self):
        start_sequence = self.current_segment_sequence
        while not self.stop_flag:
            if start_sequence in self.data_map.keys():
                # with open(self.current_save_path, "ab") as f:
                #     f.write(self.data_map[start_sequence])
                async with aiofiles.open(self.current_save_path, 'ab') as afp:
                    await afp.write(self.data_map[start_sequence])
                    # await afp.write("world")
                print(f"({self.model_name}) Write data to {self.current_save_path} success")
                _  = self.data_map.pop(start_sequence)
                del _
                start_sequence += 1
            else:
                print(f"({self.model_name}) No data to write, check again after 20s ...")
                await asyncio.sleep(20)
        
    def _get_sequence(self,partUri:str):
        # get sequence from partUri
        pat = re.compile(r'_(\d+)_')
        if pat.search(partUri):
            return int(pat.search(partUri).group(1))
        else:
            return None

class Task(TaskMixin):

    def __init__(self, model_name, save_dir):
        self.model_name = model_name
        self.stop_flag = False 
        self.has_start = False
        self.save_dir = os.path.join(save_dir, model_name,datetime.datetime.now().strftime("%Y-%m-%d"))
        super().__init__()
        
    def __delete__(self):
        self.stop_flag = True
        self.has_start = False
        self.part_to_down.clear()
        self.part_down_finish.clear()
        self.data_map.clear()

    async def start(self):
        m3u8_uri,stream_name = await self.is_online(self.model_name)
        if m3u8_uri and stream_name:
            self.online_mu3u8_uri = m3u8_uri
            self.stream_name = stream_name
            await self.get_play_list(m3u8_uri)

            loop = asyncio.get_event_loop()
            loop.create_task(self._start_downloader())
            loop.create_task(self._start_writer())
        else:
            print(f"{self.model_name} is not online,stop task... check after 20s")
            self.stop_flag = True
            await asyncio.sleep(2)
            self.has_start = False
            raise ModelOfflineError(self.model_name,f"({self.model_name}) is not online")

        while not self.stop_flag:
            self.has_start = True
            try:
                await self.get_play_list(self.online_mu3u8_uri)
            # except FlagNotSameError:
            #     logger.error("ext_x_map is not the same, begin restart after 5s ...")
            #     self.current_segment_sequence=0
            #     await asyncio.sleep(5)
            except:
                logger.exception("Error while starting task,begin restart after 5s ...", exc_info=True)
                self.current_segment_sequence=0
                await asyncio.sleep(5)


def get_config(config_file):
    with open(config_file) as f:
        return json.load(f)

class TaskManager:
    task_map = {}

    def __init__(self,config_file) -> None:
        self.config = config_file

    def add_task(self, task: Task):
        if task.model_name not in self.task_map:
            print(f"({task.model_name}) Add new model -> {task.model_name}")
            self.task_map[task.model_name] = task
            loop = asyncio.get_event_loop()
            loop.create_task(task.start()).add_done_callback(self.on_task_done)
        else:
            return

    async def run_forever(self):
        config = get_config(self.config)
        if config['proxy']['enable']:
            os.environ['HTTP_PROXY'] = config['proxy']['uri']
            os.environ['HTTPS_PROXY'] = config['proxy']['uri']
        while True:
            config = get_config(self.config)
            for model in config['models']:
                task = Task(model['name'], config["save_dir"])
                self.add_task(task)
            print("reload config after 20s... current running models -> ", list(self.task_map.keys()))
            await asyncio.sleep(20)

    
    def on_task_done(self, future):
        # print(f"Task done -> {future.result()}") # if future.result() error will raise exception
        error = future.exception()
        if error:
            if isinstance(error, ModelOfflineError):
                print(f"({error.model_name}) Model is offline delete task...")
                t =  self.task_map.pop(error.model_name)
                del t
            else:
                print(f"Task error -> {error}")
        # else:
        #     print(f"({self.}) Task done success")

if __name__ == "__main__":
    config_file = "./config.json"
    manager = TaskManager(config_file)
    loop = asyncio.get_event_loop()
    loop.run_until_complete(manager.run_forever())
    loop.close()