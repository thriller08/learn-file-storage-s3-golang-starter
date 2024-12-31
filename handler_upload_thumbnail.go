package main

import (
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

	maxMemory := int64(10 << 20)
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't parse upload from form", err)
		return
	}

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid thumbnail data", err)
		return
	}

	mimeType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't parse mime type", err)
		return
	}
	fileExt := ""
	switch mimeType {
	case "image/png":
		fileExt = "png"
	case "image/jpeg":
		fileExt = "jpg"
	default:
		respondWithError(w, http.StatusUnauthorized, "Invalid type", err)
		return
	}

	filePath := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%s.%s", videoID, fileExt))
	f, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't create file", err)
		return
	}
	defer f.Close()

	_, err = io.Copy(f, file)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't save file data", err)
		return
	}

	dbVideo, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't fetch metadata for video", err)
		return
	}

	fileUrl := fmt.Sprintf("http://localhost:%s/assets/%s.%s", cfg.port, videoID, fileExt)
	dbVideo.ThumbnailURL = &fileUrl

	err = cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't update database", err)
		return
	}

	respondWithJSON(w, http.StatusOK, dbVideo)
}
