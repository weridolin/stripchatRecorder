import m3u8
import aiohttp
import json
import logging
import asyncio
import os
import datetime
import re
import aiofiles
import requests
import hashlib
import logging
import base64
from typing import List, Dict


# logger will be configured in TaskManager after reading config
logger = logging.getLogger('logger')

def setup_logger(log_level='DEBUG'):
    """Setup logger with specified level from config"""
    level_map = {
        'DEBUG': logging.DEBUG,
        'INFO': logging.INFO,
        'WARNING': logging.WARNING,
        'ERROR': logging.ERROR
    }
    
    # Clear existing handlers
    logger.handlers.clear()
    
    logger.setLevel(level_map.get(log_level.upper(), logging.INFO))
    
    # Console handler
    sh = logging.StreamHandler()
    sh.setLevel(level_map.get(log_level.upper(), logging.INFO))
    formatter = logging.Formatter('%(asctime)s - %(name)s - %(levelname)s - %(message)s')
    sh.setFormatter(formatter)
    
    # File handler
    fh = logging.FileHandler("./err_record.log", encoding="utf-8")
    fh.setLevel(logging.DEBUG)  # File always logs debug and above
    fh.setFormatter(formatter)
    
    logger.addHandler(sh)
    logger.addHandler(fh)
    
    return logger

header = {
    'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3'
}



def get_psch_pkey_from_m3u8(m3u8_content:str):
    for line in m3u8_content.splitlines():
        if line.startswith('#EXT-X-MOUFLON:PSCH:'): #!#EXT-X-MOUFLON:PSCH:v1:Zokee2OhPh9kugh4
            parts = line.split(':')
            return parts[2],parts[3]
    return None,None

async def get_decrypt_key(pkey):
    async with aiohttp.ClientSession(trust_env=True,headers=header) as session:
        async with session.get('https://hu.stripchat.com/api/front/v3/config/static') as resp:
            resp = await resp.json()
            static_data = resp.get('static')
            mmp_origin = static_data['features']['MMPExternalSourceOrigin']
            mmp_version = static_data['featuresV2']['playerModuleExternalLoading']['mmpVersion']
            mmp_base = f"{mmp_origin}/v{mmp_version}"
            async with session.get(f"{mmp_base}/main.js") as resp:
                main_js = await resp.text()
                doppio_js = re.findall('require[(]"./(Doppio.*?[.]js)"[)]', main_js)[0]
                async with session.get(f"{mmp_base}/{doppio_js}") as resp:
                    
                    doppio_js = await resp.text()
                    decrypt_key = re.findall(f'"{pkey}:(.*?)"', doppio_js)[0]
                    return decrypt_key


def decode(encrypted_b64: str, key: str) -> str:
    hash_bytes = hashlib.sha256(key.encode("utf-8")).digest()
    hash_len = len(hash_bytes)
    encrypted_data = base64.b64decode(encrypted_b64 + "==")
    decrypted_bytes = bytearray()
    for i, cipher_byte in enumerate(encrypted_data):
        key_byte = hash_bytes[i % hash_len]
        decrypted_byte = cipher_byte ^ key_byte
        decrypted_bytes.append(decrypted_byte)

    plaintext = decrypted_bytes.decode("utf-8")
    return plaintext



def extract_mouflon_and_parts(m3u8_content: str) -> List[Dict[str, str]]:
    """
    从 m3u8 内容中提取 #EXT-X-MOUFLON 和对应的 #EXT-X-PART
    
    返回一个列表，每项是 {"mouflon": "...", "part": "..."}
    """
    lines = m3u8_content.strip().splitlines()
    result = []

    mouflon_value = None
    for line in lines:
        line = line.strip()
        if line.startswith("#EXT-X-MOUFLON:FILE:"):
            # 提取 MOUFLON 内容
            mouflon_value = line.split(":", 2)[2]  # 从第三个冒号开始取
        elif line.startswith("#EXT-X-PART:") and mouflon_value:
            # 提取 PART URI
            match = re.search(r'URI="([^"]+)"', line)
            if match:
                part_uri = match.group(1)
                result.append((mouflon_value,part_uri))
            mouflon_value = None  # 重置，保证一一对应
    
    return result

def extract_variant_playlists(m3u8_content: str) -> Dict[str, str]:
    """
    从 master m3u8 提取所有分辨率 -> 子 m3u8 URL
    """
    lines = m3u8_content.strip().splitlines()
    result = {}
    current_name = None

    for line in lines:
        line = line.strip()
        if line.startswith("#EXT-X-STREAM-INF:"):
            # 提取 NAME="480p" 这种标记
            match = re.search(r'NAME="([^"]+)"', line)
            if match:
                current_name = match.group(1)
        elif line and not line.startswith("#"):
            # 遇到 URL，和上一个 NAME 绑定
            if current_name:
                result[current_name] = line
                current_name = None

    return result


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
        self.stream_name = None # 当前stream names
        self.part_to_down = dict() # 等待下载的片段列表 
        self.part_down_finish = dict() # 正在下载的片段列表
        self.data_map = {} # 存储下载的数据 seq:list[data]
        self.temp_data_map = {} # 临时存储part0-part2的数据 seq:{part_num:data}
        self.current_save_path = None # 保存路径
        self.decrypt_key_map = {} ## #EXT-X-MOUFLON的解密key
    async def is_online(self,model_name):
        try:
            async with aiohttp.ClientSession(trust_env=True,headers=header) as session:
                async with session.get(f'https://stripchat.com/api/front/v2/models/username/{model_name}/cam') as resp:
                    resp = await resp.json()
            m3u8_file = None
            if 'cam' in resp.keys():
                if 'isCamAvailable' in resp['cam'].keys() and resp['cam']['isCamAvailable']:
                    #!新的m3u8文件接口增加了pkey/psch/playlistType参数
                    # m3u8_file = f'https://media-hls.doppiocdn.com/b-hls-19/{resp["cam"]["streamName"]}/{resp["cam"]["streamName"]}.m3u8?psch=v1&pkey=Zokee2OhPh9kugh4&playlistType=lowLatency'
                    online_mu3u8_uri = f'https://edge-hls.doppiocdn.com//hls/{resp["cam"]["streamName"]}/master/{resp["cam"]["streamName"]}_auto.m3u8'
                    stream_name = resp["cam"]["streamName"]
                    return online_mu3u8_uri,stream_name
                else:   
                    return False,None
            return False,None
        except:
            logger.error("Error while checking if model is online", exc_info=True)
            return False,None


    async def get_play_list(self,m3u8_file):
        try:
            async with aiohttp.ClientSession(trust_env=True,headers=header) as session:
                logger.debug(f"({self.model_name}) get m3u8 file -> {m3u8_file}")
                async with session.get(m3u8_file) as resp:
                    res = await resp.text()
                    ## 获取 pkey 和 psch
                    psch,pkey = get_psch_pkey_from_m3u8(res)
                    variant_playlists = extract_variant_playlists(res)
                    media_uri = variant_playlists.get('480p',variant_playlists.get("source"))
                    if not psch:
                        logger.error(f"({self.model_name}) get psch and pkey from m3u8 file failed, m3u8 file -> {m3u8_file}")
                        raise FlagNotSameError
                    if not pkey:
                        logger.error(f"({self.model_name}) get psch and pkey from m3u8 file failed, m3u8 file -> {m3u8_file}")
                        raise FlagNotSameError 
                    if not media_uri:
                        logger.error(f"({self.model_name}) get media uri from m3u8 file failed, m3u8 file -> {m3u8_file}")
                        raise FlagNotSameError

                    logger.debug(f"({self.model_name}) get pkey -> {pkey}")

                    if not self.decrypt_key_map.get(pkey):
                        decrypt_key = await get_decrypt_key(pkey)
                        self.decrypt_key_map[pkey] = decrypt_key
                    else:
                        decrypt_key = self.decrypt_key_map[pkey]

                    logger.debug(f"({self.model_name}) get decrypt key : {decrypt_key} from pkey -> {pkey}")


                    media_uri = f"{media_uri}?psch={psch}&pkey={pkey}&playlistType=lowLatency"
                    logger.debug(f"({self.model_name}) get absolute media uri -> {media_uri}")

                async with session.get(media_uri) as resp:
                    res = await resp.text()
                    m3u8_obj = m3u8.loads(res)
                    self.current_segment_sequence = m3u8_obj.media_sequence
                    self.ext_x_map = m3u8_obj.segments[0].init_section.uri

                    mouflon_and_parts = extract_mouflon_and_parts(res)
                    for mouflon, part in mouflon_and_parts:
                        real_part_url = decode(mouflon,decrypt_key)
                        real_part_url = f"{part.rsplit('/',1)[0]}/{real_part_url}"
                        sequence = self._get_sequence(real_part_url)
                        if sequence not in self.part_to_down:
                            self.part_to_down[sequence] = []
                        if real_part_url not in self.part_to_down[sequence]:
                            self.part_to_down[sequence].append(real_part_url)
                            logger.debug(f"({self.model_name}) 添加分片链接 -> {real_part_url} 到 sequence -> {sequence}")
            # return m3u8_obj
        except Exception as e:
            logger.error(f"({self.model_name}) get m3u8 file error -> {e}",exc_info=True)
            self.stop_flag= True    
            return self
    
    async def download_part_file(self,sequence,part_uri_list):
        try:
            logger.debug(f"begin down part uri list -> {part_uri_list} , sequence -> {sequence}")
            res = list()
            async with aiohttp.ClientSession(trust_env=True,headers=header) as session:
                for part_uri in part_uri_list:
                    async with session.get(part_uri) as resp:
                        if resp.status == 200:
                            # 获取原生的二进制响应数据
                            data = await resp.read()
                            res.append(data)
                            logger.debug(f"({self.model_name}) Downloading {part_uri} success")
                        else:
                            logger.debug(f"({self.model_name}) Downloading {part_uri} failed , status code -> {resp.status},response -> {await resp.text()}")
                            continue  
                logger.info(f"({self.model_name}) Downloading part file {part_uri_list} success")           
        except:
            logger.error("Error while downloading part file,ignore this file", exc_info=True)
        finally:
            self.data_map[sequence] = res
            self.part_down_finish[sequence] = part_uri_list
            # 保持part_down_finish字典只包含最近100个记录
            if len(self.part_down_finish) > 100:
                # 删除最旧的记录
                oldest_keys = sorted(self.part_down_finish.keys())[:-100]
                for key in oldest_keys:
                    del self.part_down_finish[key]
            
    async def _start_downloader(self):
        try:
            if not os.path.exists(self.save_dir):
                os.makedirs(self.save_dir)
            if self.ext_x_map:
                ## 下载init文件
                self.current_save_path = os.path.join(self.save_dir,self.ext_x_map.rsplit('/')[-1])
                if not os.path.exists(self.current_save_path):
                    async with aiohttp.ClientSession(trust_env=True,headers=header) as session:
                        async with session.get(self.ext_x_map) as resp:
                            if resp.status == 200:
                                logger.info(f"({self.model_name}) Downloading init file {self.ext_x_map} to {self.current_save_path} success...") 
                                with open(self.current_save_path, "ab") as f:
                                    f.write(await resp.read())
                            else:
                                logger.error(f"({self.model_name}) Downloading init file {self.ext_x_map} failed , status code -> {resp.status},response -> {await resp.text()}")              
                    logger.info(f"({self.model_name}) down load init file finish... begin to down part file...")
        except:
            logger.error("Error while downloading init file", exc_info=True)
            self.stop_flag = True
            return
        
        while not self.stop_flag:
            if len(self.part_to_down) == 0:
                logger.debug(f"({self.model_name}) No part to download")
                await asyncio.sleep(0)
            else:
                min_sequence = min(self.part_to_down.keys())
                max_sequence = max(self.part_to_down.keys())
                ## 下载从 min_sequence 到 max_sequence -1 的part
                for sequence in range(min_sequence, max_sequence):
                    if sequence in self.part_to_down.keys():
                        part_uri_list = self.part_to_down.pop(sequence)
                        loop=asyncio.get_event_loop()
                        # loop.create_task(self.download_part_file(sequence,part_uri_list))
                        await self.download_part_file(sequence,part_uri_list)
                        await asyncio.sleep(0)

    async def _start_writer(self):
        start_sequence = self.current_segment_sequence
        while not self.stop_flag:
            if start_sequence in self.data_map.keys():
                if not self.current_save_path:
                    logger.debug(f"({self.model_name}) Save path is not set, wait... -> {start_sequence}")
                    await asyncio.sleep(5)
                    continue
                async with aiofiles.open(self.current_save_path, 'ab') as afp:
                    for data in self.data_map[start_sequence]:
                        await afp.write(data)
                logger.info(f"({self.model_name}) Write sequence {start_sequence} to file success")
                _  = self.data_map.pop(start_sequence)
                del _
                start_sequence += 1
            else:
                await asyncio.sleep(5)
                logger.debug(f"({self.model_name}) wait 5s to get data...,current sequence -> {start_sequence}")
                # wait 10s ,if still not get the data, ignore this sequence
                if start_sequence in self.data_map.keys():
                    continue
                else:
                    start_sequence += 1
                    ## delete ignore data
                    for key in list(self.data_map.keys()):
                        if key < start_sequence:
                            logger.debug(f"({self.model_name}) Delete ignore data -> {key}")
                            _ = self.data_map.pop(key)
                            del _
                    if len(self.data_map.keys()) > 15:
                        start_sequence = min(self.data_map.keys())
        # write all rest data
        if self.data_map.keys():
            logger.debug(f"({self.model_name}) Write rest data to {self.current_save_path}...keys:{self.data_map.keys()}")
            start = min(self.data_map.keys())
            while self.data_map.keys():
                if start in self.data_map.keys():
                    async with aiofiles.open(self.current_save_path, 'ab') as afp:
                        for data in self.data_map[start]:
                            await afp.write(data)
                    logger.info(f"({self.model_name}) Write sequence {start} to file success")
                    _  = self.data_map.pop(start)
                    del _
                start += 1

    def _get_sequence(self,partUri:str):
        # get sequence from partUri
        pat = re.compile(r'_(\d+)_')
        if pat.search(partUri):
            return int(pat.search(partUri).group(1))
        else:
            return None
    
    def _get_part_number(self, partUri: str):
        # get part number from partUri (part0, part1, part2, part3)
        pat = re.compile(r'part(\d+)\.mp4')
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
        self.temp_data_map.clear()

    async def start(self):
        m3u8_uri,stream_name = await self.is_online(self.model_name)
        if m3u8_uri and stream_name:
            self.online_mu3u8_uri = m3u8_uri
            self.stream_name = stream_name
            await self.get_play_list(m3u8_uri)

        else:
            logger.debug(f"{self.model_name} is not online,stop task... check after 20s")
            self.stop_flag = True
            await asyncio.sleep(2)
            self.has_start = False
            return self

        loop = asyncio.get_event_loop()
        loop.create_task(self._start_downloader()).add_done_callback(self._on_downloader_done)
        loop.create_task(self._start_writer()).add_done_callback(self._on_writer_done)

        while not self.stop_flag:
            self.has_start = True
            await self.get_play_list(self.online_mu3u8_uri)
            await asyncio.sleep(0)
        
        return self

    def _on_downloader_done(self, future):
        error = future.exception()
        if error:
            logger.error(f"({self.model_name}) Downloader error -> {error}")
        else:
            logger.info(f"({self.model_name}) Downloader done...")


    def _on_writer_done(self, future):
        error = future.exception()
        import sys
        if error:
            logger.error(f"({self.model_name}) Writer error -> {error}",exc_info=True)
        else:
            logger.info(f"({self.model_name}) Writer done...")


def get_config(config_file):
    with open(config_file) as f:
        return json.load(f)

class TaskManager:
    task_map = {}

    def __init__(self,config_file) -> None:
        self.config = config_file

    def add_task(self, task: Task):
        if task.model_name not in self.task_map:
            logger.info(f"({task.model_name}) Start new model task -> {task.model_name}")
            self.task_map[task.model_name] = task
            loop = asyncio.get_event_loop()
            loop.create_task(task.start()).add_done_callback(self.on_task_done)
        else:
            logger.debug(f"({task.model_name}) Model is already running, ignore... has_start: {self.task_map[task.model_name].has_start}")
            return

    async def run_forever(self):
        config = get_config(self.config)
        
        # Setup logger with level from config
        log_level = config.get('log', {}).get('level', 'INFO')
        setup_logger(log_level)
        logger.info(f"Logger initialized with level: {log_level}")
        
        if config['proxy']['enable']:
            os.environ['HTTP_PROXY'] = config['proxy']['uri']
            os.environ['HTTPS_PROXY'] = config['proxy']['uri']
        while True:
            config = get_config(self.config)
            for model in config['models']:
                task = Task(model['name'], config["save_dir"])
                self.add_task(task)
            logger.debug("reload config after 20s... current running models -> %s", list(self.task_map.keys()))
            await asyncio.sleep(20)

    
    def on_task_done(self, future):
        t:Task = future.result()
        t =  self.task_map.pop(t.model_name)
        logger.info(f"({t.model_name}) task done")
        del t



def extract_preload_uris_from_str(m3u8_content: str):
    """
    从 m3u8 字符串中提取 #EXT-X-PRELOAD-HINT 的 URI
    :param m3u8_content: m3u8 的完整文本内容
    :return: list[str] 提取到的 URI 列表
    """
    preload_uris = []
    pattern = re.compile(r'URI="([^"]+)"')

    for line in m3u8_content.splitlines():
        line = line.strip()
        if line.startswith("#EXT-X-PRELOAD-HINT"):
            m = pattern.search(line)
            if m:
                preload_uris.append(m.group(1))

    return preload_uris


if __name__ == "__main__":
    config_file = "./config.json"
    manager = TaskManager(config_file)
    loop = asyncio.get_event_loop()
    loop.run_until_complete(manager.run_forever())
    loop.close()
    # asyncio.run(test())