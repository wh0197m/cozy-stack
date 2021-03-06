package thumbnail

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/vfs"
)

type imageEvent struct {
	Verb string      `json:"Verb"`
	Doc  vfs.FileDoc `json:"Doc"`
}

var formats = map[string]string{
	"small":  "640x480",
	"medium": "1280x720",
	"large":  "1920x1080",
}

var formatsNames = []string{
	"small",
	"medium",
	"large",
}

func init() {
	jobs.AddWorker(&jobs.WorkerConfig{
		WorkerType:   "thumbnail",
		Concurrency:  runtime.NumCPU(),
		MaxExecCount: 2,
		Timeout:      30 * time.Second,
		WorkerFunc:   Worker,
	})
}

// Worker is a worker that creates thumbnails for photos and images.
func Worker(ctx *jobs.WorkerContext) error {
	var img imageEvent
	if err := ctx.UnmarshalEvent(&img); err != nil {
		return err
	}
	if img.Verb != "DELETED" && img.Doc.Trashed {
		return nil
	}

	log := ctx.Logger()
	log.Debugf("thumbnail: %s %s", img.Verb, img.Doc.ID())
	i, err := instance.Get(ctx.Domain())
	if err != nil {
		return err
	}
	switch img.Verb {
	case "CREATED":
		return generateThumbnails(ctx, i, &img.Doc)
	case "UPDATED":
		if err = removeThumbnails(i, &img.Doc); err != nil {
			log.Debugf("failed to remove thumbnails for %s: %s", img.Doc.ID(), err)
		}
		return generateThumbnails(ctx, i, &img.Doc)
	case "DELETED":
		return removeThumbnails(i, &img.Doc)
	}
	return fmt.Errorf("Unknown type %s for image event", img.Verb)
}

func generateThumbnails(ctx *jobs.WorkerContext, i *instance.Instance, img *vfs.FileDoc) error {
	fs := i.ThumbsFS()
	var in io.Reader
	in, err := i.VFS().OpenFile(img)
	if err != nil {
		return err
	}

	var env []string
	{
		var tempDir string
		tempDir, err = ioutil.TempDir("", "magick")
		if err == nil {
			defer os.RemoveAll(tempDir) // #nosec
			envTempDir := fmt.Sprintf("MAGICK_TEMPORARY_PATH=%s", tempDir)
			env = []string{envTempDir}
		}
	}

	in, err = recGenerateThub(ctx, in, fs, img, "large", env, false)
	if err != nil {
		return err
	}
	in, err = recGenerateThub(ctx, in, fs, img, "medium", env, false)
	if err != nil {
		return err
	}
	_, err = recGenerateThub(ctx, in, fs, img, "small", env, true)
	return err
}

func recGenerateThub(ctx *jobs.WorkerContext, in io.Reader, fs vfs.Thumbser, img *vfs.FileDoc, format string, env []string, noOuput bool) (r io.Reader, err error) {
	defer func() {
		if inCloser, ok := in.(io.Closer); ok {
			if errc := inCloser.Close(); errc != nil && err == nil {
				err = errc
			}
		}
	}()
	file, err := fs.CreateThumb(img, format)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var buffer *bytes.Buffer
	var out io.Writer
	if noOuput {
		out = file
	} else {
		buffer = new(bytes.Buffer)
		out = io.MultiWriter(file, buffer)
	}
	err = generateThumb(ctx, in, out, format, env)
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

// The thumbnails are generated with ImageMagick, because it has the better
// compromise for speed, quality and ease of deployment.
// See https://github.com/fawick/speedtest-resize
//
// We are using some complicated ImageMagick options to optimize the speed and
// quality of the generated thumbnails.
// See https://www.smashingmagazine.com/2015/06/efficient-image-resizing-with-imagemagick/
func generateThumb(ctx *jobs.WorkerContext, in io.Reader, out io.Writer, format string, env []string) error {
	convertCmd := config.GetConfig().Jobs.ImageMagickConvertCmd
	if convertCmd == "" {
		convertCmd = "convert"
	}
	args := []string{
		"-limit", "Memory", "2GB",
		"-limit", "Map", "3GB",
		"-",              // Takes the input from stdin
		"-auto-orient",   // Rotate image according to the EXIF metadata
		"-strip",         // Strip the EXIF metadata
		"-quality", "82", // A good compromise between file size and quality
		"-interlace", "none", // Don't use progressive JPEGs, they are heavier
		"-thumbnail", formats[format], // Makes a thumbnail that fits inside the given format
		"-colorspace", "sRGB", // Use the colorspace recommended for web, sRGB
		"jpg:-", // Send the output on stdout, in JPEG format
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, convertCmd, args...) // #nosec
	cmd.Env = env
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		ctx.Logger().
			WithField("stderr", stderr.String()).
			Errorf("imagemagick failed: %s", err)
		return err
	}
	return nil
}

func removeThumbnails(i *instance.Instance, img *vfs.FileDoc) error {
	return i.ThumbsFS().RemoveThumbs(img, formatsNames)
}
