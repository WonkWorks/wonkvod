package mediaprobe

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeNormalizesSourceNeutralFacts(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "ffprobe")
	document := `{"streams":[{"index":0,"codec_name":"hevc","profile":"Main","codec_type":"video","width":1920,"height":1080,"coded_width":1920,"coded_height":1080,"pix_fmt":"yuv420p","level":120,"chroma_location":"left","refs":1,"avg_frame_rate":"24000/1001","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","disposition":{"default":1}},{"index":1,"codec_name":"aac","profile":"LC","codec_type":"audio","sample_rate":"48000","channels":6,"channel_layout":"5.1","disposition":{"default":1},"tags":{"language":"eng","title":"Surround"}},{"index":2,"codec_name":"subrip","codec_type":"subtitle","disposition":{},"tags":{"language":"eng"}}],"format":{"format_name":"matroska,webm","duration":"5651.114000"}}`
	contents := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '" + document + "'\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	facts, err := Probe(context.Background(), bytes.NewReader(make([]byte, 16384)), 1465406864)
	if err != nil {
		t.Fatal(err)
	}
	if facts.State != "inspected" || facts.Container != "mkv" || facts.DurationMS != 5651114 || facts.BitrateKbps != 2075 || facts.ProbeBytes != 16384 {
		t.Fatalf("facts=%#v", facts)
	}
	if len(facts.Video) != 1 || facts.Video[0].Codec != "hevc" || facts.Video[0].BitDepth != 8 || facts.Video[0].FrameRate < 23.97 || facts.Video[0].FrameRate > 23.98 {
		t.Fatalf("video=%#v", facts.Video)
	}
	if len(facts.Audio) != 1 || facts.Audio[0].Channels != 6 || facts.Audio[0].SampleRate != 48000 {
		t.Fatalf("audio=%#v", facts.Audio)
	}
	if len(facts.Subtitles) != 1 || facts.Subtitles[0].Codec != "subrip" {
		t.Fatalf("subtitles=%#v", facts.Subtitles)
	}
}
