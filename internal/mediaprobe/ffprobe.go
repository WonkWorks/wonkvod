// Package mediaprobe performs bounded, lazy inspection of media streams.
package mediaprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	canonical "wonkvod/internal/catalog"
)

const MaxProbeBytes int64 = 8 << 20

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}

type probeDocument struct {
	Streams []struct {
		Index          int    `json:"index"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
		CodedWidth     int    `json:"coded_width"`
		CodedHeight    int    `json:"coded_height"`
		Level          int    `json:"level"`
		Refs           int    `json:"refs"`
		Channels       int    `json:"channels"`
		Codec          string `json:"codec_name"`
		Profile        string `json:"profile"`
		Type           string `json:"codec_type"`
		PixelFormat    string `json:"pix_fmt"`
		ChromaLocation string `json:"chroma_location"`
		FrameRate      string `json:"avg_frame_rate"`
		SampleRate     string `json:"sample_rate"`
		ChannelLayout  string `json:"channel_layout"`
		Bitrate        string `json:"bit_rate"`
		ColorSpace     string `json:"color_space"`
		ColorTransfer  string `json:"color_transfer"`
		ColorPrimaries string `json:"color_primaries"`
		Disposition    struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
		Tags struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
		Bitrate  string `json:"bit_rate"`
	} `json:"format"`
}

func Probe(ctx context.Context, input io.Reader, size int64) (canonical.MediaFacts, error) {
	if input == nil {
		return canonical.MediaFacts{}, errors.New("media probe: nil input")
	}
	started := time.Now()
	reader := &countingReader{reader: io.LimitReader(input, MaxProbeBytes)}
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-probesize", strconv.FormatInt(MaxProbeBytes, 10), "-analyzeduration", "10000000", "-show_format", "-show_streams", "-of", "json", "-i", "pipe:0")
	cmd.Stdin = reader
	output, err := cmd.Output()
	if err != nil {
		return canonical.MediaFacts{State: "retryable", ProbeBytes: reader.count, ProbeMS: time.Since(started).Milliseconds()}, fmt.Errorf("media probe: ffprobe: %w", err)
	}
	var document probeDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return canonical.MediaFacts{State: "retryable", ProbeBytes: reader.count, ProbeMS: time.Since(started).Milliseconds()}, fmt.Errorf("media probe: decode: %w", err)
	}
	facts := canonical.MediaFacts{State: "inspected", Container: normalizeContainer(document.Format.Name), Size: size, ObservedAt: time.Now().UTC(), ProbeBytes: reader.count, ProbeMS: time.Since(started).Milliseconds()}
	duration, _ := strconv.ParseFloat(document.Format.Duration, 64)
	if duration > 0 {
		facts.DurationMS = int64(math.Round(duration * 1000))
	}
	bitrate, _ := strconv.ParseInt(document.Format.Bitrate, 10, 64)
	if bitrate <= 0 && size > 0 && duration > 0 {
		bitrate = int64(math.Round(float64(size*8) / duration))
	}
	facts.BitrateKbps = int((bitrate + 500) / 1000)
	for _, value := range document.Streams {
		switch value.Type {
		case "video":
			facts.Video = append(facts.Video, canonical.VideoStream{Index: value.Index, Codec: value.Codec, Profile: strings.ToLower(value.Profile), Level: value.Level, Width: value.Width, Height: value.Height, CodedWidth: value.CodedWidth, CodedHeight: value.CodedHeight, FrameRate: rational(value.FrameRate), BitDepth: pixelBitDepth(value.PixelFormat), PixelFormat: value.PixelFormat, ChromaLocation: value.ChromaLocation, ChromaSubsampling: chromaSubsampling(value.PixelFormat), RefFrames: value.Refs, ColorSpace: value.ColorSpace, ColorTransfer: value.ColorTransfer, ColorPrimaries: value.ColorPrimaries, Default: value.Disposition.Default == 1})
		case "audio":
			sampleRate, _ := strconv.Atoi(value.SampleRate)
			streamBitrate, _ := strconv.Atoi(value.Bitrate)
			facts.Audio = append(facts.Audio, canonical.AudioStream{Index: value.Index, Codec: value.Codec, Profile: strings.ToLower(value.Profile), Channels: value.Channels, ChannelLayout: value.ChannelLayout, SampleRate: sampleRate, BitrateKbps: (streamBitrate + 500) / 1000, Language: value.Tags.Language, Title: value.Tags.Title, Default: value.Disposition.Default == 1})
		case "subtitle":
			facts.Subtitles = append(facts.Subtitles, canonical.TextStream{Index: value.Index, Codec: value.Codec, Language: value.Tags.Language, Title: value.Tags.Title, Default: value.Disposition.Default == 1, Forced: value.Disposition.Forced == 1})
		}
	}
	if facts.Container == "" || len(facts.Video)+len(facts.Audio) == 0 {
		facts.State = "retryable"
		return facts, errors.New("media probe: incomplete stream description")
	}
	return facts, nil
}

func normalizeContainer(value string) string {
	if strings.Contains(value, "matroska") {
		return "mkv"
	}
	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = value[:comma]
	}
	return strings.TrimSpace(value)
}
func rational(value string) float64 {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		result, _ := strconv.ParseFloat(value, 64)
		return result
	}
	n, _ := strconv.ParseFloat(parts[0], 64)
	d, _ := strconv.ParseFloat(parts[1], 64)
	if d == 0 {
		return 0
	}
	return n / d
}
func pixelBitDepth(value string) int {
	for _, marker := range []struct {
		text  string
		depth int
	}{{"12", 12}, {"10", 10}, {"9", 9}} {
		if strings.Contains(value, marker.text) {
			return marker.depth
		}
	}
	if value != "" {
		return 8
	}
	return 0
}
func chromaSubsampling(value string) string {
	if strings.Contains(value, "420") {
		return "4:2:0"
	}
	if strings.Contains(value, "422") {
		return "4:2:2"
	}
	if strings.Contains(value, "444") {
		return "4:4:4"
	}
	return ""
}
