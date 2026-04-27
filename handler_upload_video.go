package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"

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

	tmpFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
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

	newPath, err := processVideoForFastStart(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't process file", err)
		return
	}
	precessedFile, err := os.Open(newPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't process file", err)
		return
	}
	defer os.Remove(newPath)
	defer precessedFile.Close()

	b := make([]byte, 32)
	rand.Read(b)
	randHash := base64.RawURLEncoding.EncodeToString(b)

	catagory, err := getVideoAspectRatio(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get aspect ratio", err)
		return
	}

	fileName := fmt.Sprintf("%s/%s.%s", catagory, randHash, fileExt)

	_, err = cfg.s3Client.PutObject(
		context.Background(),
		&s3.PutObjectInput{
			Bucket:      &cfg.s3Bucket,
			Key:         &fileName,
			Body:        precessedFile,
			ContentType: &contentType,
		},
	)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload", err)
		return
	}

	vidUrl := fmt.Sprintf(
		"%s,%s",
		cfg.s3Bucket,
		fileName,
	)
	vid.VideoURL = &vidUrl

	if err := cfg.db.UpdateVideo(vid); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update database", err)
		return
	}

	vid, err = cfg.dbVideoToSignedVideo(vid)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate video url", err)
		return
	}

	respondWithJSON(w, http.StatusCreated, vid)
}

func getVideoAspectRatio(path string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error", "-print_format", "json", "-show_streams", path,
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

func processVideoForFastStart(filePath string) (string, error) {
	newPath := fmt.Sprint(filePath, ".processed")
	cmd := exec.Command(
		"ffmpeg",
		"-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", newPath,
	)
	buf := bytes.Buffer{}
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		log.Printf("command failed with: %s", buf.String())
		return "", err
	}

	return newPath, nil
}

func generatePresignedURL(
	s3Client *s3.Client,
	bucket,
	key string,
	expiryTime time.Duration,
) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	preReq, err := presignClient.PresignGetObject(
		context.Background(),
		&s3.GetObjectInput{Bucket: &bucket, Key: &key}, s3.WithPresignExpires(expiryTime),
	)
	if err != nil {
		return "", err
	}
	return preReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, errors.New("Err: nil videoUrl field")
	}
	tmp := strings.Split(*video.VideoURL, ",")
	if len(tmp) != 2 {
		return video, fmt.Errorf("Err: %v, in videoUrl field", tmp)
	}
	bucket, key := tmp[0], tmp[1]

	vidUrl, err := generatePresignedURL(cfg.s3Client, bucket, key, time.Minute*30)
	if err != nil {
		return video, err
	}
	video.VideoURL = &vidUrl

	return video, nil
}
