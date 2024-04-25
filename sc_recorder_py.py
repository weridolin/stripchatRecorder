import m3u8
import requests
import aiohttp
import json
import logging
import asyncio
import os
import datetime

os.environ['HTTP_PROXY'] = 'http://127.0.0.1:10809'
os.environ['HTTPS_PROXY'] = 'http://127.0.0.1:10809'

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)


class FlagNotSameError(Exception):
    pass

class TaskMixin:

    def __init__(self) -> None:
        self.ext_x_map = None  # 同次直播流对应的唯一标记
        self.online_mu3u8_uri = None ## 直播流地址
        self.current_segment_sequence = 0 # 当前直播流第几个序列片段
        self.stream_name = None # 当前stream name
        self.part_to_down = list() # 等待下载的片段列表 
        self.part_down_finish = list() # 正在下载的片段列表

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
                    print(f"New segment sequence -> {m3u8_obj.media_sequence}")
                    for segment in m3u8_obj.segments:
                        if self.ext_x_map and segment.init_section.uri != self.ext_x_map:
                            self.ext_x_map = segment.init_section.uri # 替换成最新的 ext_x_map 
                            self.current_segment_sequence = 0
                            raise FlagNotSameError(f"({self.model_name}) xt_x_map is not the same")
                        if segment.uri not in self.part_to_down and  segment.uri not in self.part_down_finish:
                            self.part_to_down.append(segment.uri)
                        print(f"add part uri -> {segment.uri}")
                        if not self.ext_x_map:
                            self.ext_x_map = segment.init_section.uri
                            # print(f"ext_x_map -> {self.ext_x_map}")
                    self.current_segment_sequence = m3u8_obj.media_sequence
                else:
                    print(f"No new segment sequence -> {m3u8_obj.media_sequence},{self.current_segment_sequence}")
        return m3u8_obj

    
    
    async def download_part_file(self,ext_x_map:str,part_uri):
        try:
            async with aiohttp.ClientSession() as session:
                async with session.get(part_uri) as resp:
                    if resp.status == 200:
                        save_file = os.path.join(self.save_dir,ext_x_map.rsplit('/')[-1])
                        print(f"Downloading {part_uri} to {save_file}")
                        with open(save_file, "ab") as f:
                            f.write(await resp.read())
                        print(f"({self.model_name}) Downloading {part_uri} to {save_file} finish")
                    else:
                        print(f"({self.model_name}) Downloading {part_uri} failed , status code -> {resp.status},response -> {await resp.text()}")
                    # self.part_downing.remove(part_uri)
                        # self.part_to_down[ext_x_map].remove(part_uri)
        except:
            logger.exception("Error while downloading part file", exc_info=True)

    async def _start_download(self):
        # if self.part_to_down:
        if not os.path.exists(self.save_dir):
            os.makedirs(self.save_dir)

        if self.ext_x_map:
            ## 下载init文件
            save_file = os.path.join(self.save_dir,self.ext_x_map.rsplit('/')[-1])
            if not os.path.exists(save_file):
                async with aiohttp.ClientSession() as session:
                    async with session.get(self.ext_x_map) as resp:
                        if resp.status == 200:
                            print(f"Downloading init file {self.ext_x_map} to {save_file}") 
                            with open(save_file, "ab") as f:
                                f.write(await resp.read())               
                print(f"down load init file finish... begin to down part file...")
        while self.part_to_down:
            part_uri = self.part_to_down.pop(0)
            self.part_down_finish.append(part_uri)
            await self.download_part_file(self.ext_x_map,part_uri)
            if self.stop_flag:
                break

class Task(TaskMixin):

    def __init__(self, model_name, save_dir):
        self.model_name = model_name
        self.stop_flag = False 
        self.has_start = False
        self.save_dir = os.path.join(save_dir, model_name,datetime.datetime.now().strftime("%Y-%m-%d"))
        super().__init__()
        

    async def start(self):
        while not self.stop_flag:
            self.has_start = True
            try:
                m3u8_uri,stream_name = await self.is_online(self.model_name)
                if m3u8_uri and stream_name:
                    self.online_mu3u8_uri = m3u8_uri
                    self.stream_name = stream_name
                    await self.get_play_list(m3u8_uri)
                    await self._start_download()
                else:
                    print(f"{self.model_name} is not online,check again after 20s ...")
                    await asyncio.sleep(20)
            except FlagNotSameError:
                logger.error("ext_x_map is not the same, begin restart after 5s ...")
                await asyncio.sleep(5)
            except:
                logger.exception("Error while starting task,begin restart after 5s ...", exc_info=True)
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
            print(f"Add new model -> {task.model_name}")
            self.task_map[task.model_name] = task
            loop = asyncio.get_event_loop()
            loop.create_task(task.start())
        else:
            return
        #task.start()

    async def run_forever(self):
        while True:
            config = get_config(self.config)
            for model in config['models']:
                task = Task(model['name'], config["save_dir"])
                self.add_task(task)
            await asyncio.sleep(10)
            print("reload config... current running models -> ", list(self.task_map.keys()))

if __name__ == "__main__":
    config_file = "./config.json"
    manager = TaskManager(config_file)
    loop = asyncio.get_event_loop()
    loop.run_until_complete(manager.run_forever())
    loop.close()
