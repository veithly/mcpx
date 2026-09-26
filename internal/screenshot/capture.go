package screenshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"time"
)

const maxImageBytes = 8 << 20

var ErrInvalidRequest = errors.New("invalid screenshot request")

type Request struct {
	Mode        string `json:"mode"`
	Display     int    `json:"display"`
	X           int    `json:"x,omitempty"`
	Y           int    `json:"y,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Compression string `json:"compression,omitempty"`
	Format      string `json:"format,omitempty"`
	Quality     int    `json:"quality,omitempty"`
	MaxWidth    int    `json:"max_width,omitempty"`
	MaxHeight   int    `json:"max_height,omitempty"`
}

type Metadata struct {
	Mode           string    `json:"mode"`
	Display        int       `json:"display"`
	X              int       `json:"x,omitempty"`
	Y              int       `json:"y,omitempty"`
	CapturedWidth  int       `json:"captured_width"`
	CapturedHeight int       `json:"captured_height"`
	OutputWidth    int       `json:"output_width"`
	OutputHeight   int       `json:"output_height"`
	Compression    string    `json:"compression"`
	Format         string    `json:"format"`
	Quality        int       `json:"quality,omitempty"`
	MIMEType       string    `json:"mime_type"`
	Bytes          int       `json:"bytes"`
	SHA256         string    `json:"sha256"`
	CapturedAt     time.Time `json:"captured_at"`
}

type Result struct {
	Metadata Metadata `json:"metadata"`
	Data     []byte   `json:"-"`
}

type nativeCapturer func(context.Context, Request, string) error

type Service struct {
	capture nativeCapturer
}

func NewService() *Service { return &Service{capture: captureNative} }

func newService(capture nativeCapturer) *Service { return &Service{capture: capture} }

func (s *Service) Capture(ctx context.Context, request Request) (Result, error) {
	request, err := normalizeRequest(request)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	temporary, err := os.CreateTemp("", "mcpx-screenshot-*.png")
	if err != nil {
		return Result{}, err
	}
	path := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		os.Remove(path)
		return Result{}, err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(path)
		return Result{}, err
	}
	defer os.Remove(path)
	if err := s.capture(ctx, request, path); err != nil {
		return Result{}, err
	}
	handle, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	imageValue, _, err := image.Decode(handle)
	handle.Close()
	if err != nil {
		return Result{}, fmt.Errorf("decode captured screen: %w", err)
	}
	captured := imageValue.Bounds()
	if captured.Dx() <= 0 || captured.Dy() <= 0 {
		return Result{}, fmt.Errorf("screen capture returned an empty image; check OS screen-recording permission")
	}
	outputWidth, outputHeight := fitDimensions(captured.Dx(), captured.Dy(), request.MaxWidth, request.MaxHeight)
	if outputWidth != captured.Dx() || outputHeight != captured.Dy() {
		imageValue = resizeNearest(imageValue, outputWidth, outputHeight)
	}
	var encoded bytes.Buffer
	mimeType := "image/png"
	if request.Format == "jpeg" {
		mimeType = "image/jpeg"
		err = jpeg.Encode(&encoded, imageValue, &jpeg.Options{Quality: request.Quality})
	} else {
		level := png.DefaultCompression
		if request.Compression == "none" {
			level = png.BestSpeed
		} else if request.Compression == "small" {
			level = png.BestCompression
		}
		err = (&png.Encoder{CompressionLevel: level}).Encode(&encoded, imageValue)
	}
	if err != nil {
		return Result{}, fmt.Errorf("encode screenshot: %w", err)
	}
	if encoded.Len() > maxImageBytes {
		return Result{}, fmt.Errorf("screenshot is %d bytes; use balanced/small compression or lower max_width/max_height", encoded.Len())
	}
	data := encoded.Bytes()
	digest := sha256.Sum256(data)
	return Result{
		Data: append([]byte(nil), data...),
		Metadata: Metadata{
			Mode: request.Mode, Display: request.Display, X: request.X, Y: request.Y,
			CapturedWidth: captured.Dx(), CapturedHeight: captured.Dy(), OutputWidth: outputWidth, OutputHeight: outputHeight,
			Compression: request.Compression, Format: request.Format, Quality: request.Quality,
			MIMEType: mimeType, Bytes: len(data), SHA256: "sha256:" + hex.EncodeToString(digest[:]), CapturedAt: time.Now().UTC(),
		},
	}, nil
}

func normalizeRequest(request Request) (Request, error) {
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	if request.Mode == "" {
		request.Mode = "fullscreen"
	}
	if request.Mode != "fullscreen" && request.Mode != "region" {
		return Request{}, fmt.Errorf("mode must be fullscreen or region")
	}
	if request.Display < 0 {
		return Request{}, fmt.Errorf("display must be zero or greater")
	}
	if request.Mode == "region" {
		if request.Width <= 0 || request.Height <= 0 {
			return Request{}, fmt.Errorf("region width and height must be positive")
		}
		if int64(request.Width)*int64(request.Height) > 100_000_000 {
			return Request{}, fmt.Errorf("region exceeds 100 megapixels")
		}
	}
	request.Compression = strings.ToLower(strings.TrimSpace(request.Compression))
	if request.Compression == "" {
		request.Compression = "balanced"
	}
	switch request.Compression {
	case "none":
		if request.Format == "" {
			request.Format = "png"
		}
	case "balanced":
		if request.Format == "" {
			request.Format = "jpeg"
		}
		if request.Quality == 0 {
			request.Quality = 82
		}
		if request.MaxWidth == 0 {
			request.MaxWidth = 1920
		}
		if request.MaxHeight == 0 {
			request.MaxHeight = 1200
		}
	case "small":
		if request.Format == "" {
			request.Format = "jpeg"
		}
		if request.Quality == 0 {
			request.Quality = 60
		}
		if request.MaxWidth == 0 {
			request.MaxWidth = 1280
		}
		if request.MaxHeight == 0 {
			request.MaxHeight = 800
		}
	default:
		return Request{}, fmt.Errorf("compression must be none, balanced, or small")
	}
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	if request.Format == "jpg" {
		request.Format = "jpeg"
	}
	if request.Format != "png" && request.Format != "jpeg" {
		return Request{}, fmt.Errorf("format must be png or jpeg")
	}
	if request.Format == "jpeg" {
		if request.Quality == 0 {
			request.Quality = 82
		}
		if request.Quality < 1 || request.Quality > 100 {
			return Request{}, fmt.Errorf("quality must be between 1 and 100")
		}
	}
	if request.MaxWidth < 0 || request.MaxHeight < 0 || request.MaxWidth > 16_384 || request.MaxHeight > 16_384 {
		return Request{}, fmt.Errorf("max_width and max_height must be between 0 and 16384")
	}
	return request, nil
}

func fitDimensions(width, height, maxWidth, maxHeight int) (int, int) {
	if (maxWidth <= 0 || width <= maxWidth) && (maxHeight <= 0 || height <= maxHeight) {
		return width, height
	}
	ratio := 1.0
	if maxWidth > 0 && width > maxWidth {
		ratio = float64(maxWidth) / float64(width)
	}
	if maxHeight > 0 && float64(height)*ratio > float64(maxHeight) {
		ratio = float64(maxHeight) / float64(height)
	}
	resultWidth, resultHeight := int(float64(width)*ratio), int(float64(height)*ratio)
	if resultWidth < 1 {
		resultWidth = 1
	}
	if resultHeight < 1 {
		resultHeight = 1
	}
	return resultWidth, resultHeight
}

func resizeNearest(source image.Image, width, height int) image.Image {
	sourceBounds := source.Bounds()
	converted := image.NewNRGBA(image.Rect(0, 0, sourceBounds.Dx(), sourceBounds.Dy()))
	draw.Draw(converted, converted.Bounds(), source, sourceBounds.Min, draw.Src)
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sourceY := y * converted.Bounds().Dy() / height
		for x := 0; x < width; x++ {
			sourceX := x * converted.Bounds().Dx() / width
			sourceOffset := converted.PixOffset(sourceX, sourceY)
			targetOffset := result.PixOffset(x, y)
			copy(result.Pix[targetOffset:targetOffset+4], converted.Pix[sourceOffset:sourceOffset+4])
		}
	}
	return result
}
