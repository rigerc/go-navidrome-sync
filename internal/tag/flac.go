package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

func ReadRating(filePath string) (int, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
		return ReadPOPMRating(filePath)
	case ".flac":
		return readFlacRating(filePath)
	default:
		return 0, nil
	}
}

func readFlacRating(filePath string) (int, error) {
	lf, err := readFlacFile(filePath)
	if err != nil {
		return 0, err
	}
	return lf.Rating, nil
}

func WriteRating(filePath string, rating int) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
		return WritePOPMRating(filePath, rating)
	case ".flac":
		return WriteFlacRating(filePath, rating)
	default:
		return fmt.Errorf("unsupported file format: %s", ext)
	}
}

type LocalFile struct {
	Rating        int
	PlayCount     int64
	Played        *time.Time
	Starred       bool
	MusicBrainzID string
	ISRC          string
	Artist        string
	Album         string
	Title         string
}

func ReadLocalFile(filePath string) (*LocalFile, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
		return readMP3File(filePath)
	case ".flac":
		return readFlacFile(filePath)
	default:
		return &LocalFile{}, nil
	}
}

func readFlacFile(filePath string) (*LocalFile, error) {
	stream, err := flac.ParseFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse FLAC %s: %w", filePath, err)
	}
	defer stream.Close()

	lf := &LocalFile{}

	for _, block := range stream.Blocks {
		vc, ok := block.Body.(*meta.VorbisComment)
		if !ok {
			continue
		}

		for _, tag := range vc.Tags {
			switch strings.ToUpper(tag[0]) {
			case "FMPS_RATING":
				if lf.Rating == 0 {
					f, err := parseFmpsRating(tag[1])
					if err == nil && f > 0 {
						lf.Rating = fmpsToStars(f)
					}
				}
			case "RATING":
				if lf.Rating == 0 {
					r, err := parseVorbisRating(tag[1])
					if err == nil && r > 0 {
						lf.Rating = r
					}
				}
			case "PLAY_COUNT":
				if n, err := strconv.ParseInt(tag[1], 10, 64); err == nil && n > 0 {
					lf.PlayCount = n
				}
			case "LAST_PLAYED":
				if t, err := time.Parse(time.RFC3339, tag[1]); err == nil {
					lf.Played = &t
				}
			case "FAVORITE", "STARRED":
				lf.Starred = isTruthyTagValue(tag[1])
			case "MUSICBRAINZ_TRACKID":
				lf.MusicBrainzID = tag[1]
			case "ISRC":
				lf.ISRC = tag[1]
			case "ARTIST":
				if lf.Artist == "" {
					lf.Artist = tag[1]
				}
			case "ALBUM":
				lf.Album = tag[1]
			case "TITLE":
				lf.Title = tag[1]
			}
		}
	}

	return lf, nil
}

type rawBlock struct {
	header [4]byte
	body   []byte
}

func WriteStarred(filePath string, starred bool) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
		return WriteMP3Starred(filePath, starred)
	case ".flac":
		return WriteFlacStarred(filePath, starred)
	default:
		return fmt.Errorf("unsupported file format: %s", ext)
	}
}

func isTruthyTagValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "favorite", "starred":
		return true
	default:
		return false
	}
}

func WritePlayStats(filePath string, playCount int64, played *time.Time) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
		return WriteMP3PlayStats(filePath, playCount, played)
	case ".flac":
		return WriteFlacPlayStats(filePath, playCount, played)
	default:
		return fmt.Errorf("unsupported file format: %s", ext)
	}
}

func WriteFlacStarred(filePath string, starred bool) error {
	return rewriteFlacVorbisComments(filePath, func(vc *meta.VorbisComment) {
		setVorbisCommentStarred(vc, starred)
	})
}

func WriteFlacPlayStats(filePath string, playCount int64, played *time.Time) error {
	return rewriteFlacVorbisComments(filePath, func(vc *meta.VorbisComment) {
		setVorbisCommentPlayStats(vc, playCount, played)
	})
}

func rewriteFlacVorbisComments(filePath string, update func(*meta.VorbisComment)) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", filePath, err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", filePath, err)
	}
	if len(data) < 4 {
		return fmt.Errorf("not a valid FLAC file: %s", filePath)
	}
	if !bytes.Equal(data[:4], []byte("fLaC")) {
		return fmt.Errorf("not a valid FLAC file: %s", filePath)
	}

	var blocks []rawBlock
	offset := 4
	for offset < len(data) {
		if offset+4 > len(data) {
			return fmt.Errorf("truncated metadata header at offset %d", offset)
		}
		hdr := [4]byte{data[offset], data[offset+1], data[offset+2], data[offset+3]}
		isLast := hdr[0]&0x80 != 0
		_ = meta.Type(hdr[0] & 0x7F)
		length := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
		bodyEnd := offset + 4 + length
		if bodyEnd > len(data) {
			return fmt.Errorf("metadata block body overflows file at offset %d", offset)
		}
		body := make([]byte, length)
		copy(body, data[offset+4:bodyEnd])
		blocks = append(blocks, rawBlock{header: hdr, body: body})
		offset = bodyEnd
		if isLast {
			break
		}
	}

	audioData := data[offset:]

	vcIdx := -1
	for i, b := range blocks {
		if meta.Type(b.header[0]&0x7F) == meta.TypeVorbisComment {
			vcIdx = i
			break
		}
	}

	var newVcBody []byte
	if vcIdx >= 0 {
		vc, err := parseVorbisCommentBody(blocks[vcIdx].body)
		if err != nil {
			return fmt.Errorf("failed to parse existing VorbisComment: %w", err)
		}
		update(vc)
		newVcBody, err = encodeVorbisCommentBody(vc)
		if err != nil {
			return fmt.Errorf("failed to encode VorbisComment: %w", err)
		}
	} else {
		vc := &meta.VorbisComment{
			Vendor: "go-navidrome-sync",
		}
		update(vc)
		newVcBody, err = encodeVorbisCommentBody(vc)
		if err != nil {
			return fmt.Errorf("failed to encode VorbisComment: %w", err)
		}
	}

	var buf bytes.Buffer
	buf.Write([]byte("fLaC"))
	for i, b := range blocks {
		isLast := i == len(blocks)-1
		if i == vcIdx {
			hdr := b.header
			if isLast {
				hdr[0] |= 0x80
			} else {
				hdr[0] &^= 0x80
			}
			hdr[1] = byte(len(newVcBody) >> 16)
			hdr[2] = byte(len(newVcBody) >> 8)
			hdr[3] = byte(len(newVcBody))
			buf.Write(hdr[:])
			buf.Write(newVcBody)
		} else {
			if isLast {
				b.header[0] |= 0x80
			}
			buf.Write(b.header[:])
			buf.Write(b.body)
		}
	}
	if vcIdx < 0 {
		hdr := [4]byte{byte(meta.TypeVorbisComment) | 0x80}
		hdr[1] = byte(len(newVcBody) >> 16)
		hdr[2] = byte(len(newVcBody) >> 8)
		hdr[3] = byte(len(newVcBody))
		buf.Write(hdr[:])
		buf.Write(newVcBody)
	}
	buf.Write(audioData)

	output := buf.Bytes()
	if vcIdx < 0 && len(blocks) > 0 {
		lastHeaderOffset := 4
		for i := 0; i < len(blocks)-1; i++ {
			lastHeaderOffset += 4 + len(blocks[i].body)
		}
		output[lastHeaderOffset] &^= 0x80
	}

	if err := os.WriteFile(filePath, output, info.Mode()); err != nil {
		return fmt.Errorf("failed to write %s: %w", filePath, err)
	}
	return nil
}

func setVorbisCommentStarred(vc *meta.VorbisComment, starred bool) {
	for i := 0; i < len(vc.Tags); i++ {
		key := strings.ToUpper(vc.Tags[i][0])
		if key == "FAVORITE" || key == "STARRED" {
			vc.Tags = append(vc.Tags[:i], vc.Tags[i+1:]...)
			i--
		}
	}
	if starred {
		vc.Tags = append(vc.Tags, [2]string{"FAVORITE", "1"})
	}
}

func setVorbisCommentPlayStats(vc *meta.VorbisComment, playCount int64, played *time.Time) {
	setTag := func(key, value string) {
		for i, tag := range vc.Tags {
			if strings.ToUpper(tag[0]) == key {
				vc.Tags[i][1] = value
				return
			}
		}
		vc.Tags = append(vc.Tags, [2]string{key, value})
	}
	if playCount > 0 {
		setTag("PLAY_COUNT", strconv.FormatInt(playCount, 10))
	}
	if played != nil {
		setTag("LAST_PLAYED", played.UTC().Format(time.RFC3339))
	}
}

func WriteFlacRating(filePath string, rating int) error {
	return rewriteFlacVorbisComments(filePath, func(vc *meta.VorbisComment) {
		setVorbisCommentRating(vc, rating)
	})
}

func setVorbisCommentRating(vc *meta.VorbisComment, rating int) {
	if rating == 0 {
		return
	}

	set := false
	for i, tag := range vc.Tags {
		if strings.ToUpper(tag[0]) == "RATING" {
			vc.Tags[i][1] = fmt.Sprintf("%d", rating)
			set = true
			break
		}
	}

	if !set {
		vc.Tags = append(vc.Tags, [2]string{"RATING", fmt.Sprintf("%d", rating)})
	}
}

func parseVorbisCommentBody(data []byte) (*meta.VorbisComment, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("vorbis comment body too short")
	}

	vendorLen := binary.LittleEndian.Uint32(data[0:4])
	offset := 4 + int(vendorLen)

	if offset+4 > len(data) {
		return nil, fmt.Errorf("vorbis comment too short for tag count")
	}

	vendor := string(data[4:offset])
	tagCount := binary.LittleEndian.Uint32(data[offset : offset+4])
	offset += 4

	vc := &meta.VorbisComment{
		Vendor: vendor,
		Tags:   make([][2]string, 0, tagCount),
	}

	for i := uint32(0); i < tagCount; i++ {
		if offset+4 > len(data) {
			break
		}

		vecLen := binary.LittleEndian.Uint32(data[offset : offset+4])
		offset += 4

		if offset+int(vecLen) > len(data) {
			break
		}

		vector := string(data[offset : offset+int(vecLen)])
		offset += int(vecLen)

		pos := strings.Index(vector, "=")
		if pos == -1 {
			continue
		}

		vc.Tags = append(vc.Tags, [2]string{vector[:pos], vector[pos+1:]})
	}

	return vc, nil
}

func encodeVorbisCommentBody(vc *meta.VorbisComment) ([]byte, error) {
	var buf bytes.Buffer

	vendor := []byte(vc.Vendor)
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(vendor))); err != nil {
		return nil, err
	}
	buf.Write(vendor)

	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(vc.Tags))); err != nil {
		return nil, err
	}

	for _, tag := range vc.Tags {
		vec := []byte(fmt.Sprintf("%s=%s", tag[0], tag[1]))
		if err := binary.Write(&buf, binary.LittleEndian, uint32(len(vec))); err != nil {
			return nil, err
		}
		buf.Write(vec)
	}

	return buf.Bytes(), nil
}

func parseFmpsRating(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

func fmpsToStars(f float64) int {
	switch {
	case f >= 0.9:
		return 5
	case f >= 0.7:
		return 4
	case f >= 0.4:
		return 3
	case f >= 0.2:
		return 2
	case f > 0:
		return 1
	default:
		return 0
	}
}

func parseVorbisRating(s string) (int, error) {
	var r int
	_, err := fmt.Sscanf(s, "%d", &r)
	if err != nil {
		return 0, err
	}
	if r < 0 || r > 5 {
		return 0, fmt.Errorf("rating %d out of range 0-5", r)
	}
	return r, nil
}
