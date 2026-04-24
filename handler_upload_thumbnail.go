package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// if user has permition over the video
	vid, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse thumbnail", err)
		return
	}

	if vid.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Couldn't parse thumbnail", err)
		return
	}

	var maxMemory int64 = 10 << 20 // 10 MB
	r.ParseMultipartForm(maxMemory)
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse thumbnail", err)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't detect thumbnail Ext", err)
		return
	}
	var fileExt string
	switch mediaType {
	case "image/png":
		fileExt = "png"
	case "image/jpeg":
		fileExt = "jpeg"
	default:
		{
			respondWithError(
				w,
				http.StatusInternalServerError,
				"Couldn't detect thumbnail Ext",
				err,
			)
			return
		}
	}

	b := make([]byte, 32)
	rand.Read(b)
	encodedThumbnail := base64.RawURLEncoding.EncodeToString(b)

	fileName := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%s.%s", encodedThumbnail, fileExt))

	createdFile, err := os.Create(fileName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create file", err)
		return
	}
	_, err = io.Copy(createdFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy thumbnail", err)
		return
	}
	thumbnailUrl := fmt.Sprintf(
		"http://localhost:%s/%s",
		cfg.port,
		fileName,
	)

	vid.ThumbnailURL = &thumbnailUrl

	if err := cfg.db.UpdateVideo(vid); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update database", err)
		return
	}

	respondWithJSON(w, http.StatusCreated, vid)
}
