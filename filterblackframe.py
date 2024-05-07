import cv2

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
    cv2.destroyAllWindows()

def is_black_frame(frame, threshold=10):
    # 计算帧中像素值的平均值
    avg_pixel_value = frame.mean()
    # 如果平均值小于阈值，认为是黑屏帧
    return avg_pixel_value < threshold


dir = "E:/records/"

# get all downloaded video files
import os
import re

for root, dirs, files in os.walk(dir):
    for file in files:
        if re.match(r".*\.mp4", file):
            if re.match(r".*after\.mp4", file):
                continue
            else:
                name, ext = os.path.splitext(file)
                input_video = os.path.join(root, file)
                output_video = os.path.join(root, name + "_after.mp4")
                if os.path.exists(output_video):
                    os.remove(output_video)
                print("deal with: ", input_video, output_video)
                remove_black_frames(input_video, output_video)
                os.remove(input_video)
