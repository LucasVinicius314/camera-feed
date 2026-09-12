# camera-feed

Receives an RTMP camera stream and displays it in a native window. No media server required.

## How it works

The app runs an RTMP server on port 1935. When a camera connects and publishes, it spawns an ffmpeg subprocess to decode the stream and renders each frame directly into the window using GDI.

## Requirements

* Windows
* Go 1.25+
* ffmpeg in PATH

## Usage

```
go run ./src
```

Point your camera at `rtmp://localhost:1935/live/sauron` .

## Keybinds

| Key | Action |
|-----|--------|
| T | Toggle always on top |
