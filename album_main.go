package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/nfnt/resize"
	"github.com/rwcarlsen/goexif/exif"
	"html/template"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const MaxThumbnailWidth = 300
const MaxThumbnailHeight = 400

var HeadTemplate *template.Template = template.Must(template.New("head").Parse(`<!DOCTYPE html>
<html lang='en'>
 <head>
  <title>{{.Name}}</title>
  <meta charset='utf-8'>
  <style>
    .img-link {
      text-decoration: none;
    }
  </style>
</head>
<body>
<h1>{{.Name}}</h1>
<p>{{.NumImages}} images in this album and subalbums.</p>`))

var ImgTemplate *template.Template = template.Must(template.New("img").Parse(`<a class="img-link" href="{{.Name}}">
  <img src="{{.Thumbnail.Name}}" alt="{{.Name}}"
    width="{{.Thumbnail.Width}}" height="{{.Thumbnail.Height}}">
</a>`))

// Time after all likely user photo times.
// Using the value from https://stackoverflow.com/a/32620397.
// (292277024627-12-06 15:30:07.999999999 +0000 UTC).
// Note: time.Time can't be a const; see https://stackoverflow.com/a/48160134.
var FutureTime = time.Unix(1<<63-62135596801, 999999999)

// Time before all likely user photo times.
// (January 1, year 1, 00:00:00.000000000 UTC).
var PastTime = time.Time{}

var inputDirFlag = flag.String("input", "", "Path to input photos directory.")
var outputDirFlag = flag.String("output", "", "Path at which to write album.")

type Thumbnail struct {
	Name   string
	Width  int
	Height int
}

type Image struct {
	Name      string
	Thumbnail Thumbnail
	DateTime  time.Time
}

type Album struct {
	Name      string
	NumImages int
	MinTime   time.Time
	MaxTime   time.Time
}

func timeToString(t time.Time) string {
	if !t.Equal(FutureTime) && !t.IsZero() {
		return fmt.Sprintf("%+v", t)
	}
	return "(time n/a)"
}

func (a Album) String() string {
	minStr := timeToString(a.MinTime)
	maxStr := timeToString(a.MaxTime)
	return fmt.Sprintf(
		"Album %s has %d image(s) from between %s and %s", a.Name, a.NumImages, minStr, maxStr)
}

func isImageFile(path string) bool {
	return strings.HasSuffix(path, "jpeg") || strings.HasSuffix(path, "jpg")
}

// readEXIFDate extracts the date from the EXIF metadata from the given file.
func readEXIFDateTime(imageBytes []byte) (time.Time, error) {
	x, err := exif.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return time.Time{}, err
	}
	return x.DateTime()
}

// createThumbnail makes a thumbnail file for the given image in outputDir.
// It returns the base filename (e.g., "pic_thumbnail.jpeg") for the new file in outputDir.
func createThumbnail(imageBytes []byte, imageName string, outputDir string) (Thumbnail, error) {
	dot := strings.LastIndexByte(imageName, '.')
	// This should be impossible; just die.
	if dot == -1 {
		log.Fatalf("imageName missing extension: %s", imageName)
	}
	thumbnailName := imageName[:dot] + "_thumbnail" + imageName[dot:]
	image, err := jpeg.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return Thumbnail{}, err
	}
	thumbnail := resize.Thumbnail(MaxThumbnailWidth, MaxThumbnailHeight, image, resize.Lanczos3)
	out, err := os.Create(filepath.Join(outputDir, thumbnailName))
	if err != nil {
		return Thumbnail{}, err
	}
	// Ignoring the advice here for now: https://www.joeshaw.org/dont-defer-close-on-writable-files/
	defer out.Close()
	if err := jpeg.Encode(out, thumbnail, nil); err != nil {
		return Thumbnail{}, err
	}
	bounds := thumbnail.Bounds()
	return Thumbnail{Name: thumbnailName, Width: bounds.Dx(), Height: bounds.Dy()}, nil
}

func processImage(inputDir string, imageName string, outputDir string, ch chan Image) {
	result := Image{Name: imageName}
	imagePath := filepath.Join(inputDir, imageName)
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		fmt.Printf("Couldn't read image %s: %+v", imagePath, err)
		os.Exit(1)
	}
	if t, err := readEXIFDateTime(imageBytes); err == nil {
		result.DateTime = t
	} else {
		log.Printf("Problem reading EXIF date-time for %s: %+v", imagePath, err)
	}
	if thumbnail, err := createThumbnail(imageBytes, imageName, outputDir); err == nil {
		result.Thumbnail = thumbnail
	} else {
		fmt.Printf("Couldn't create thumbnail for image %s: %+v", imagePath, err)
		os.Exit(1)
	}
	// Finally, copy the full size image to the new location.
	copyName := filepath.Join(outputDir, imageName)
	if err := os.WriteFile(copyName, imageBytes, 0750); err != nil {
		fmt.Printf("Couldn't create copy %s: %+v", copyName, err)
		os.Exit(1)
	}
	ch <- result
}

func processImages(inputDir string, imageNames []string, outputDir string) []Image {
	n := len(imageNames)
	result := make([]Image, n)
	ch := make(chan Image, n)
	for _, name := range imageNames {
		go processImage(inputDir, name, outputDir, ch)
	}
	for i := 0; i < n; i++ {
		result[i] = <-ch
	}
	return result
}

func writeHtml(album Album, subAlbums []Album, images []Image, outputDir string) {
	var buf bytes.Buffer
	HeadTemplate.Execute(&buf, album)
	for _, image := range images {
		ImgTemplate.Execute(&buf, image)
	}
	htmlFile := filepath.Join(outputDir, "index.html")
	if err := os.WriteFile(htmlFile, buf.Bytes(), 0750); err != nil {
		fmt.Printf("Couldn't write index.html %s: %+v", htmlFile, err)
		os.Exit(1)
	}
}

// createAlbum recursively walks intputDir, outputs images + HTML in outputDir.
// Returns true if it created any output.
func createAlbum(inputDir string, outputDir string) Album {
	result := Album{Name: filepath.Base(inputDir), MinTime: FutureTime, MaxTime: PastTime}
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		fmt.Printf("Couldn't read dir %s: %+v", inputDir, err)
		os.Exit(1)
	}
	var imageNames []string
	var subAlbums []Album
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			subAlbum := createAlbum(filepath.Join(inputDir, name), filepath.Join(outputDir, name))
			if subAlbum.NumImages > 0 {
				subAlbums = append(subAlbums, subAlbum)
				result.NumImages += subAlbum.NumImages
				if result.MinTime.After(subAlbum.MinTime) {
					result.MinTime = subAlbum.MinTime
				}
				if result.MaxTime.Before(subAlbum.MaxTime) {
					result.MaxTime = subAlbum.MaxTime
				}
			}
		} else if isImageFile(name) {
			imageNames = append(imageNames, name)
		}
	}
	if len(imageNames) > 0 {
		if err := os.MkdirAll(outputDir, 0750); err != nil {
			fmt.Printf("Couldn't make output dir %s: %+v", outputDir, err)
			os.Exit(1)
		}
	}
	images := processImages(inputDir, imageNames, outputDir)
	for _, image := range images {
		if image.DateTime.IsZero() {
			continue
		}
		if result.MinTime.After(image.DateTime) {
			result.MinTime = image.DateTime
		}
		if result.MaxTime.Before(image.DateTime) {
			result.MaxTime = image.DateTime
		}
	}
	result.NumImages += len(images)
	writeHtml(result, subAlbums, images, outputDir)
	return result
}

func main() {
	flag.Parse()
	album := createAlbum(*inputDirFlag, *outputDirFlag)
	fmt.Printf("%s\n", album.String())
}
