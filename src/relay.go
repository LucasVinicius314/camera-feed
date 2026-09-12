package main

import (
	"bytes"
	"context"
	"sync"

	flvtag "github.com/yutopp/go-flv/tag"
	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

type relayService struct {
	mu      sync.Mutex
	streams map[string]*pubsub
}

func newRelayService() *relayService {
	return &relayService{streams: make(map[string]*pubsub)}
}

func (r *relayService) getOrCreate(name string) *pubsub {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ps, ok := r.streams[name]; ok {
		return ps
	}
	ps := &pubsub{}
	r.streams[name] = ps
	return ps
}

func (r *relayService) get(name string) (*pubsub, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps, ok := r.streams[name]
	return ps, ok
}

type pubsub struct {
	mu           sync.Mutex
	subs         []*sub
	avcSeqHeader *flvtag.FlvTag
	lastKeyFrame *flvtag.FlvTag
}

func (ps *pubsub) publish(flv *flvtag.FlvTag) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	switch flv.Data.(type) {
	case *flvtag.VideoData:
		d := flv.Data.(*flvtag.VideoData)
		if d.AVCPacketType == flvtag.AVCPacketTypeSequenceHeader {
			ps.avcSeqHeader = cloneFlv(flv)
		}
		if d.FrameType == flvtag.FrameTypeKeyFrame {
			ps.lastKeyFrame = cloneFlv(flv)
		}
		for _, s := range ps.subs {
			if !s.initialized {
				if ps.avcSeqHeader != nil {
					_ = s.send(cloneFlv(ps.avcSeqHeader))
				}
				if ps.lastKeyFrame != nil {
					_ = s.send(cloneFlv(ps.lastKeyFrame))
				}
				s.initialized = true
			}
			_ = s.send(cloneFlv(flv))
		}
	default:
		for _, s := range ps.subs {
			_ = s.send(cloneFlv(flv))
		}
	}
}

func (ps *pubsub) addSub(s *sub) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.subs = append(ps.subs, s)
}

func (ps *pubsub) removeSub(s *sub) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i, v := range ps.subs {
		if v == s {
			ps.subs = append(ps.subs[:i], ps.subs[i+1:]...)
			return
		}
	}
}

type sub struct {
	conn          *rtmp.Conn
	streamID      uint32
	initialized   bool
	lastTimestamp uint32
}

func (s *sub) send(flv *flvtag.FlvTag) error {
	if flv.Timestamp != 0 && s.lastTimestamp == 0 {
		s.lastTimestamp = flv.Timestamp
	}
	flv.Timestamp -= s.lastTimestamp

	buf := new(bytes.Buffer)
	ctx := context.Background()

	switch flv.Data.(type) {
	case *flvtag.AudioData:
		d := flv.Data.(*flvtag.AudioData)
		if err := flvtag.EncodeAudioData(buf, d); err != nil {
			return err
		}
		return s.conn.Write(ctx, 5, flv.Timestamp, &rtmp.ChunkMessage{
			StreamID: s.streamID,
			Message:  &rtmpmsg.AudioMessage{Payload: buf},
		})
	case *flvtag.VideoData:
		d := flv.Data.(*flvtag.VideoData)
		if err := flvtag.EncodeVideoData(buf, d); err != nil {
			return err
		}
		return s.conn.Write(ctx, 6, flv.Timestamp, &rtmp.ChunkMessage{
			StreamID: s.streamID,
			Message:  &rtmpmsg.VideoMessage{Payload: buf},
		})
	case *flvtag.ScriptData:
		d := flv.Data.(*flvtag.ScriptData)
		if err := flvtag.EncodeScriptData(buf, d); err != nil {
			return err
		}
		amdBuf := new(bytes.Buffer)
		amfEnc := rtmpmsg.NewAMFEncoder(amdBuf, rtmpmsg.EncodingTypeAMF0)
		if err := rtmpmsg.EncodeBodyAnyValues(amfEnc, &rtmpmsg.NetStreamSetDataFrame{
			Payload: buf.Bytes(),
		}); err != nil {
			return err
		}
		return s.conn.Write(ctx, 8, flv.Timestamp, &rtmp.ChunkMessage{
			StreamID: s.streamID,
			Message: &rtmpmsg.DataMessage{
				Name:     "@setDataFrame",
				Encoding: rtmpmsg.EncodingTypeAMF0,
				Body:     amdBuf,
			},
		})
	}
	return nil
}

func cloneFlv(flv *flvtag.FlvTag) *flvtag.FlvTag {
	v := *flv
	switch flv.Data.(type) {
	case *flvtag.AudioData:
		d := *v.Data.(*flvtag.AudioData)
		d.Data = bytes.NewBuffer(d.Data.(*bytes.Buffer).Bytes())
		v.Data = &d
	case *flvtag.VideoData:
		d := *v.Data.(*flvtag.VideoData)
		d.Data = bytes.NewBuffer(d.Data.(*bytes.Buffer).Bytes())
		v.Data = &d
	case *flvtag.ScriptData:
		d := *v.Data.(*flvtag.ScriptData)
		v.Data = &d
	}
	return &v
}
