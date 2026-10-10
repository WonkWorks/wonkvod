package catalog

import "time"

// MediaFacts is a source- and endpoint-neutral description obtained lazily
// from the media bytes. It is deliberately small enough to persist with the
// canonical catalog; media payload bytes are never stored.
type MediaFacts struct {
	State       string        `json:"state,omitempty"`
	Container   string        `json:"container,omitempty"`
	DurationMS  int64         `json:"duration_ms,omitempty"`
	BitrateKbps int           `json:"bitrate_kbps,omitempty"`
	Size        int64         `json:"size,omitempty"`
	Video       []VideoStream `json:"video,omitempty"`
	Audio       []AudioStream `json:"audio,omitempty"`
	Subtitles   []TextStream  `json:"subtitles,omitempty"`
	ObservedAt  time.Time     `json:"observed_at,omitempty"`
	ProbeBytes  int64         `json:"probe_bytes,omitempty"`
	ProbeMS     int64         `json:"probe_ms,omitempty"`
}

type VideoStream struct {
	Index             int     `json:"index"`
	Codec             string  `json:"codec"`
	Profile           string  `json:"profile,omitempty"`
	Level             int     `json:"level,omitempty"`
	Width             int     `json:"width,omitempty"`
	Height            int     `json:"height,omitempty"`
	CodedWidth        int     `json:"coded_width,omitempty"`
	CodedHeight       int     `json:"coded_height,omitempty"`
	FrameRate         float64 `json:"frame_rate,omitempty"`
	BitDepth          int     `json:"bit_depth,omitempty"`
	PixelFormat       string  `json:"pixel_format,omitempty"`
	ChromaLocation    string  `json:"chroma_location,omitempty"`
	ChromaSubsampling string  `json:"chroma_subsampling,omitempty"`
	RefFrames         int     `json:"ref_frames,omitempty"`
	ColorSpace        string  `json:"color_space,omitempty"`
	ColorTransfer     string  `json:"color_transfer,omitempty"`
	ColorPrimaries    string  `json:"color_primaries,omitempty"`
	Default           bool    `json:"default,omitempty"`
}

type AudioStream struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec"`
	Profile       string `json:"profile,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BitrateKbps   int    `json:"bitrate_kbps,omitempty"`
	Language      string `json:"language,omitempty"`
	Title         string `json:"title,omitempty"`
	Default       bool   `json:"default,omitempty"`
}

type TextStream struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}
