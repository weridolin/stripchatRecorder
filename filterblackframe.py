import cv2
from moviepy.editor import VideoFileClip
import os,subprocess,re,sys




dir = r"E:\records"

def merge():
    for model in os.listdir(dir):
        if model!="after":
            for date in os.listdir(os.path.join(dir, model)):
                output_file = f"./{date}" + ".mp4"
                date_dir = os.path.join(dir, model, date)
                print("deal with: ", date_dir)
                file_name_list = os.listdir(date_dir)
                ## sort by create time
                file_name_list.sort(key=lambda x: os.path.getctime(os.path.join(date_dir, x)))
                print(file_name_list)
                input_args = ""
                for file in file_name_list:
                    path = os.path.join(date_dir, file)
                    input_args += f"-i {path} "
                command = f"ffmpeg {input_args} -filter_complex concat=n={len(file_name_list)}:v=1:a=0 -f mp4 {output_file}"
                print(command)
                p = subprocess.run(command, shell=True)
                p.wait()
                return
            

def merge1():
    from moviepy.editor import VideoFileClip
    from moviepy.video.io.ffmpeg_tools import ffmpeg_concat

    # 分段加载和处理视频
    def process_video(video_path):
        return VideoFileClip(video_path)

    # 视频文件列表
    for model in os.listdir(dir):
        if model!="after":
            for date in os.listdir(os.path.join(dir, model)):
                output_file = f"./{date}" + ".mp4"
                date_dir = os.path.join(dir, model, date)
                print("deal with: ", date_dir)
                file_name_list = os.listdir(date_dir)

                # 临时保存的中间文件列表
                temp_files = []
  


                # 逐个处理并保存中间结果
                # for idx, video_file in enumerate(file_name_list):
                #     path = os.path.join(date_dir, video_file)
                #     clip = process_video(path)
                #     temp_filename = os.path.join(date_dir,f"temp_{idx}.mp4") 
                #     clip.write_videofile(temp_filename, codec="libx264")
                #     temp_files.append(temp_filename)
                #     clip.close()

                # # 合并中间结果文件
                # output_file = "merged_video.mp4"
                # ffmpeg_concat(temp_files, output_file, vcodec="libx264")

                # # 删除临时文件
                # import os
                # for temp_file in temp_files:
                #     os.remove(temp_file)

                # print(f"合并完成，输出文件: {output_file}")
                # break



for root, dirs, files in os.walk(dir):
    for file in files:
        if "_after.mp4" in file:
            print("skip -------------------------------------------> : ", file)
            continue
        name, ext = os.path.splitext(file)
        input_video = os.path.join(root, file)
        output_video = os.path.join(root, name + "_after.mp4")
        if os.path.exists(output_video):
            print("skip -------------------------------------------> : ", input_video)
            continue
        print("deal with: ", input_video, output_video)
        ffmpeg_exec = os.path.join(os.path.dirname(__file__), "ffmpeg.exe")
        cmd = f"""{ffmpeg_exec} -i {input_video} -vf "blackframe=99:32" -vf "trim=start_frame=100" {output_video}"""
        p = subprocess.Popen(cmd, shell=True, stdout=sys.stdout, stderr=sys.stderr)
        p.wait()
        os.remove(input_video)   

# merge1()
