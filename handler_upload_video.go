package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"

	// "github.com/docker/cli/cli/compose/schema/data"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	var maxSize int64 = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	vid, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse thumbnail", err)
		return
	}

	if vid.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Couldn't parse thumbnail", err)
		return
	}
	if err := r.ParseMultipartForm(maxSize); err != nil {
		respondWithError(w, http.StatusBadRequest, "Parse Error", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Video not found", err)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't detect video Ext", err)
		return
	}
	var fileExt string
	switch mediaType {
	case "video/mp4":
		fileExt = "mp4"
	default:
		{
			respondWithError(
				w,
				http.StatusInternalServerError,
				"Couldn't detect video Ext",
				err,
			)
			return
		}
	}

	tmpFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create file", err)
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy file", err)
		return
	}
	tmpFile.Seek(0, io.SeekStart)

	b := make([]byte, 32)
	rand.Read(b)
	encodedVid := base64.RawURLEncoding.EncodeToString(b)

	prefix, err := getVideoAspectRatio(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get aspect ratio", err)
		return
	}

	fileName := fmt.Sprintf("%s/%s.%s", prefix, encodedVid, fileExt)
	_, err = cfg.s3Client.PutObject(
		context.Background(),
		&s3.PutObjectInput{
			Bucket:      &cfg.s3Bucket,
			Key:         &fileName,
			Body:        tmpFile,
			ContentType: &contentType,
		},
	)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload", err)
		return
	}

	vidUrl := fmt.Sprintf(
		"https://%s.s3.%s.amazonaws.com/%s",
		cfg.s3Bucket,
		cfg.s3Region,
		fileName,
	)

	vid.VideoURL = &vidUrl

	if err := cfg.db.UpdateVideo(vid); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update database", err)
		return
	}

	respondWithJSON(w, http.StatusCreated, vid)
}

func getVideoAspectRatio(path string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		[]string{"-v", "error", "-print_format", "json", "-show_streams", path}...,
	)
	buf := bytes.Buffer{}
	cmd.Stdout = &buf

	if err := cmd.Run(); err != nil {
		return "", err
	}

	data := struct {
		Streams []struct {
			DisplayAspectRatio string `json:"display_aspect_ratio"`
		} `json:"streams"`
	}{}

	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		return "", err
	}

	aspectRatio := data.Streams[0].DisplayAspectRatio

	var catagory string
	switch aspectRatio {
	case "16:9":
		catagory = "landscape"
	case "9:16":
		catagory = "portrait"
	default:
		catagory = "other"
	}
	return catagory, nil
}
