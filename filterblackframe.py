import cv2
from moviepy.editor import VideoFileClip
import os,subprocess,re,sys

def remove_black_frames(input_video, output_video):
    cap = cv2.VideoCapture(input_video)
    if not cap.isOpened():
        print("Error: Unable to open input video")
        return
    
    frame_width = int(cap.get(cv2.CAP_PROP_FRAME_WIDTH))
    frame_height = int(cap.get(cv2.CAP_PROP_FRAME_HEIGHT))
    fps = cap.get(cv2.CAP_PROP_FPS)
    total_frames = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))

    fourcc = cv2.VideoWriter_fourcc(*'mp4v')
    out = cv2.VideoWriter(output_video, fourcc, fps, (frame_width, frame_height))

    black_frame_threshold = 5  # 定义黑屏帧阈值

    while cap.isOpened():
        ret, frame = cap.read()
        if not ret:
            break
        
        # 检测黑屏帧
        if is_black_frame(frame):
            continue

        out.write(frame)

    cap.release()
    out.release()


def is_black_frame(frame, threshold=10):
    # 计算帧中像素值的平均值
    avg_pixel_value = frame.mean()
    # 如果平均值小于阈值，认为是黑屏帧
    return avg_pixel_value < threshold


# remove_black_frames(r"E:\records\CNNANAoo\2024-05-07\69756356_init_SUZUtRNC9AaHEAMD.mp4", r"E:\records\CNNANAoo\2024-05-07\69756356_init_SUZUtRNC9AaHEAMD_after.mp4")

dir = "E:/records/"



# def deal_by_ffmpeg(input_video, output_video):
#     import ffmpeg
#     # 定义过滤器链
#     blackframe_filter = "blackframe=99:32"
#     trim_filter = "trim=start_frame=100"

#     # 构建 ffmpeg 命令
#     cmd = (
#         ffmpeg.input(input_video,executable=r"D:\fireFoxDownload\ffmpeg-7.0-essentials_build\ffmpeg-7.0-essentials_build\bin\ffmpeg.exe")
#         .filter(blackframe_filter)
#         .filter(trim_filter)
#         .output(output_video)
#         .run()
#     )
# deal_by_ffmpeg(r"E:\records\CNNANAoo\2024-05-07\69756356_init_SUZUtRNC9AaHEAMD.mp4", r"E:\records\CNNANAoo\2024-05-07\69756356_init_SUZUtRNC9AaHEAMD_after.mp4")

import time
max_process = os.cpu_count()-2
processes = []
import threading

# class Task(threading.Thread):

#     def __init__(self,input_video,output_video):
#         threading.Thread.__init__(self)
#         self.input_video = input_video
#         self.output_video = output_video

#     def run(self):
#         print("deal with: ", self.input_video, self.output_video)
#         if os.path.exists(self.output_video):
#             os.remove(self.output_video)
#         print("deal with: ", self.input_video, self.output_video)
#         ffmpeg_exec = os.path.join(os.path.dirname(__file__), "ffmpeg.exe")
#         cmd = f"""{ffmpeg_exec} -i {self.input_video} -vf "blackframe=99:32" -vf "trim=start_frame=100" {self.output_video}"""
#         p = subprocess.Popen(cmd, shell=True, stdout=sys.stdout, stderr=sys.stderr)
#         p.wait()
#         os.remove(self.input_video)          

# todo_list=[]

# for root, dirs, files in os.walk(dir):
#     for file in files:
#         name, ext = os.path.splitext(file)
#         input_video = os.path.join(root, file)
#         output_video = os.path.join(root, name + "_after.mp4")
#         todo_list.append((input_video,output_video))

# while todo_list:
#     if len(processes) <= max_process:
#         item = todo_list.pop()
#         t = Task(item[0],item[1])
#         t.start()
#         processes.append(t)
#         continue
#     for p in processes:
#         if not p.is_alive():
#             processes.remove(p)
#             break
#     time.sleep(1)


