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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/thriller08/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/thriller08/learn-file-storage-s3-golang-starter/internal/database"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	maxVideoSize := int64(1 << 30)

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

	fmt.Println("uploading video for video", videoID, "by user", userID)

	dbVideo, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't fetch metadata for video", err)
		return
	}
	if dbVideo.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User not authorized", err)
		return
	}

	err = r.ParseMultipartForm(maxVideoSize)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't parse video upload", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid video data", err)
		return
	}
	defer file.Close()

	mimeType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil || mimeType != "video/mp4" {
		respondWithError(w, http.StatusUnsupportedMediaType, "Invalid video upload", err)
		return
	}

	tmpFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temporary file", err)
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	_, err = io.Copy(tmpFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save video data", err)
		return
	}

	formatPrefix, err := getVideoAspectRatio(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't determine aspect ratio", err)
		return
	}

	processedPath, err := processVideoForFastStart(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return
	}

	procFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed video", err)
		return
	}
	defer os.Remove(procFile.Name())
	defer procFile.Close()

	buf := make([]byte, 32)
	_, err = rand.Read(buf)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create filename", err)
		return
	}

	fileKey := fmt.Sprintf("%s/%s.mp4", formatPrefix, base64.RawURLEncoding.EncodeToString(buf))
	_, err = procFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error seeking in temp file", err)
		return
	}

	s3Input := s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileKey,
		Body:        procFile,
		ContentType: &mimeType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &s3Input)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save to S3", err)
		return
	}

	commaUrl := fmt.Sprintf("%s,%s", cfg.s3Bucket, fileKey)
	dbVideo.VideoURL = &commaUrl

	err = cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't update database", err)
		return
	}

	viewVideo, err := cfg.dbVideoToSignedVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get presigned video link", err)
		return
	}

	respondWithJSON(w, http.StatusOK, viewVideo)
}

func getVideoAspectRatio(filePath string) (string, error) {
	out := new(bytes.Buffer)

	type videoData struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	cmd.Stdout = out
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	data := videoData{}
	dec := json.NewDecoder(out)
	err = dec.Decode(&data)
	if err != nil || len(data.Streams) < 1 {
		return "", err
	}

	if data.Streams[0].Width*9 == data.Streams[0].Height*16 {
		return "landscape", nil
	} else if data.Streams[0].Width*16 == data.Streams[0].Height*9 {
		return "portrait", nil
	} else {
		return "other", nil
	}
}

func processVideoForFastStart(filePath string) (string, error) {
	outPath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outPath)
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	return outPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	client := s3.NewPresignClient(s3Client)

	fetchInput := s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}
	video, err := client.PresignGetObject(context.Background(), &fetchInput, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}

	return video.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	x := strings.Split(*video.VideoURL, ",")
	if len(x) > 2 {
		return database.Video{}, fmt.Errorf("invalid bucket and key found")
	}
	bucket, key := x[0], x[1]

	tempUrl, err := generatePresignedURL(cfg.s3Client, bucket, key, time.Minute*5)
	if err != nil {
		return database.Video{}, err
	}

	video.VideoURL = &tempUrl

	return video, nil
}
