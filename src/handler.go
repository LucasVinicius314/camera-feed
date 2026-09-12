package main

import (
	"bytes"
	"io"
	"log"

	flvtag "github.com/yutopp/go-flv/tag"
	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

type handler struct {
	rtmp.DefaultHandler
	relay       *relayService
	conn        *rtmp.Conn
	ps          *pubsub
	sub         *sub
	loggedAudio bool
	loggedVideo bool
}

func (h *handler) OnServe(conn *rtmp.Conn) {
	h.conn = conn
}

func (h *handler) OnPublish(_ *rtmp.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPublish) error {
	log.Printf("camera connected: stream=%s", cmd.PublishingName)
	h.ps = h.relay.getOrCreate(cmd.PublishingName)
	go startDecoder()
	return nil
}

func (h *handler) OnPlay(ctx *rtmp.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPlay) error {
	log.Printf("player subscribed: stream=%s", cmd.StreamName)
	ps, ok := h.relay.get(cmd.StreamName)
	if !ok {
		ps = h.relay.getOrCreate(cmd.StreamName)
	}
	s := &sub{conn: h.conn, streamID: ctx.StreamID}
	ps.addSub(s)
	h.sub = s
	return nil
}

func (h *handler) OnSetDataFrame(_ uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
	if h.ps == nil {
		return nil
	}
	r := bytes.NewReader(data.Payload)
	var script flvtag.ScriptData
	if err := flvtag.DecodeScriptData(r, &script); err != nil {
		return nil
	}
	h.ps.publish(&flvtag.FlvTag{TagType: flvtag.TagTypeScriptData, Data: &script})
	return nil
}

func (h *handler) OnAudio(timestamp uint32, payload io.Reader) error {
	if h.ps == nil {
		return nil
	}
	var audio flvtag.AudioData
	if err := flvtag.DecodeAudioData(payload, &audio); err != nil {
		return err
	}
	body := new(bytes.Buffer)
	if _, err := io.Copy(body, audio.Data); err != nil {
		return err
	}
	audio.Data = body
	if !h.loggedAudio {
		log.Printf("audio flowing: format=%v", audio.SoundFormat)
		h.loggedAudio = true
	}
	h.ps.publish(&flvtag.FlvTag{TagType: flvtag.TagTypeAudio, Timestamp: timestamp, Data: &audio})
	return nil
}

func (h *handler) OnVideo(timestamp uint32, payload io.Reader) error {
	if h.ps == nil {
		return nil
	}
	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
		return err
	}
	body := new(bytes.Buffer)
	if _, err := io.Copy(body, video.Data); err != nil {
		return err
	}
	video.Data = body
	if !h.loggedVideo {
		log.Printf("video flowing: codec=%v frameType=%v", video.CodecID, video.FrameType)
		h.loggedVideo = true
	}
	h.ps.publish(&flvtag.FlvTag{TagType: flvtag.TagTypeVideo, Timestamp: timestamp, Data: &video})
	return nil
}

func (h *handler) OnClose() {
	log.Println("camera disconnected")
	if h.sub != nil && h.ps != nil {
		h.ps.removeSub(h.sub)
	}
}
