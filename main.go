package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	outDir       = "downloads"
	updateTicker *time.Ticker
	config       Config
)

type Config struct {
	DownloadPath     string `json:"downloadPath"`
	UpdateIntervalHr int    `json:"updateIntervalHr"`
}

type Podcast struct {
	PodcastId   string           `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Url         string           `json:"url"`
	Thumbnails  []Thumbnail      `json:"thumbnails"`
	Episodes    []PodcastEpisode `json:"entries"`
}

type PodcastEpisode struct {
	EpisodeId        string           `json:"id"`
	Title            string           `json:"title"`
	Thumbnails       []Thumbnail      `json:"thumbnails"`
	Duration         int              `json:"duration"`
	Url              string           `json:"url"`
	DownloadingState DownloadingState `json:"downloadingState"`
}

type Thumbnail struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type DownloadingState struct {
	Downloaded    bool `json:"downloaded"`
	IsDownloading bool `json:"isDownloading"`
	HasToDownload bool `json:"hasToDownload"`
}

func main() {
	loadConfig()

	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("Error creating data directory: %v", err)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		log.Fatalf("Error creating downloads directory: %v", err)
	}

	startUpdateScheduler()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	r.StaticFile("/", "./index.html")
	r.StaticFile("/index.html", "./index.html")

	r.GET("/api/podcasts", handleGetPodcasts)
	r.POST("/api/podcast/add", handleAddPodcast)
	r.DELETE("/api/podcast/delete", handleDeletePodcast)
	r.POST("/api/episode/download", handleDownloadEpisode)
	r.GET("/api/config", handleGetConfig)
	r.POST("/api/config", handlePostConfig)
	r.POST("/api/update-now", handleUpdateNow)

	fmt.Println("Server starting on http://localhost:8080")
	r.Run(":8080")
}

func loadConfig() {
	data, err := os.ReadFile("config.json")
	if err != nil {
		config = Config{
			DownloadPath:     "downloads",
			UpdateIntervalHr: 5,
		}
		saveConfig()
		return
	}
	json.Unmarshal(data, &config)
	outDir = config.DownloadPath
}

func saveConfig() error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("config.json", data, 0644)
}

func startUpdateScheduler() {
	if updateTicker != nil {
		updateTicker.Stop()
	}

	go runUpdateAndDownloadAll()

	updateTicker = time.NewTicker(time.Duration(config.UpdateIntervalHr) * time.Hour)
	go func() {
		for range updateTicker.C {
			runUpdateAndDownloadAll()
		}
	}()
}

func handleGetPodcasts(c *gin.Context) {
	files, err := filepath.Glob("data/*.json")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var podcasts []Podcast
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var p Podcast
		if json.Unmarshal(data, &p) == nil {
			podcasts = append(podcasts, p)
		}
	}

	c.JSON(http.StatusOK, podcasts)
}

func handleAddPodcast(c *gin.Context) {
	var req struct {
		Url             string `json:"url"`
		DownloadHistory bool   `json:"downloadHistory"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	go addNewPodcast(req.Url, req.DownloadHistory)
	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

func handleDeletePodcast(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
		return
	}
	os.Remove(fmt.Sprintf("data/%s.json", id))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func handleDownloadEpisode(c *gin.Context) {
	var req struct {
		PodcastId string `json:"podcastId"`
		EpisodeId string `json:"episodeId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	go func() {
		p, err := loadPodcast(req.PodcastId)
		if err != nil {
			return
		}

		for i := range p.Episodes {
			if p.Episodes[i].EpisodeId == req.EpisodeId {
				p.Episodes[i].DownloadingState.HasToDownload = true
				savePodcastFile(p, req.PodcastId)
				runDownloadForPodcast(req.PodcastId)
				break
			}
		}
	}()

	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

func handleGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, config)
}

func handlePostConfig(c *gin.Context) {
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	saveConfig()
	outDir = config.DownloadPath
	os.MkdirAll(outDir, 0755)
	startUpdateScheduler()
	c.JSON(http.StatusOK, gin.H{"status": "saved"})
}

func handleUpdateNow(c *gin.Context) {
	go runUpdateAndDownloadAll()
	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

func addNewPodcast(podcastUrl string, shouldDownloadHistoryEpisodes bool) {
	cmd := exec.Command("yt-dlp", "--flat-playlist", "-J", podcastUrl)
	out, err := cmd.Output()
	if err != nil {
		fmt.Println("Error fetching playlist:", err)
		return
	}

	var podcast Podcast
	err = json.Unmarshal(out, &podcast)
	if err != nil {
		fmt.Println("Error parsing playlist JSON:", err)
		return
	}

	podcast.Url = podcastUrl
	for i := range podcast.Episodes {
		podcast.Episodes[i].DownloadingState.HasToDownload = shouldDownloadHistoryEpisodes
	}

	jsonData, _ := json.MarshalIndent(podcast, "", "  ")
	filePath := filepath.Join("data", fmt.Sprintf("%s.json", podcast.PodcastId))
	os.WriteFile(filePath, jsonData, 0644)

	if shouldDownloadHistoryEpisodes {
		runDownloadForPodcast(podcast.PodcastId)
	}
}

func fetchPodcastFromUrl(podcastUrl string) (Podcast, error) {
	cmd := exec.Command("yt-dlp", "--flat-playlist", "-J", podcastUrl)
	out, err := cmd.Output()
	if err != nil {
		return Podcast{}, err
	}

	var p Podcast
	json.Unmarshal(out, &p)
	p.Url = podcastUrl
	return p, nil
}

func updatePodcast(podcastId string) {
	filePath := fmt.Sprintf("data/%s.json", podcastId)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var existing Podcast
	json.Unmarshal(data, &existing)

	if existing.Url == "" {
		return
	}

	latest, err := fetchPodcastFromUrl(existing.Url)
	if err != nil {
		return
	}

	existingIds := make(map[string]bool)
	for _, e := range existing.Episodes {
		existingIds[e.EpisodeId] = true
	}

	added := false
	for _, e := range latest.Episodes {
		if e.EpisodeId == "" {
			continue
		}
		if !existingIds[e.EpisodeId] {
			e.DownloadingState = DownloadingState{HasToDownload: true}
			existing.Episodes = append([]PodcastEpisode{e}, existing.Episodes...)
			added = true
		}
	}

	if added {
		outJSON, _ := json.MarshalIndent(existing, "", "  ")
		os.WriteFile(filePath, outJSON, 0644)
	}
}

func downloadEpisode(podcastEpisode PodcastEpisode, podcastName string) (PodcastEpisode, error) {
	os.MkdirAll(filepath.Join(outDir, podcastName), 0755)
	cmd := exec.Command("yt-dlp", "-x", "--audio-format", "mp3",
		"-o", filepath.Join(outDir, podcastName, "%(title)s.%(ext)s"),
		podcastEpisode.Url)

	if err := cmd.Run(); err != nil {
		return podcastEpisode, err
	}
	podcastEpisode.DownloadingState.Downloaded = true
	podcastEpisode.DownloadingState.IsDownloading = false
	podcastEpisode.DownloadingState.HasToDownload = false
	return podcastEpisode, nil
}

func loadPodcast(podcastId string) (Podcast, error) {
	data, err := os.ReadFile("data/" + podcastId + ".json")
	if err != nil {
		return Podcast{}, err
	}

	var p Podcast
	json.Unmarshal(data, &p)
	return p, nil
}

func runUpdateAndDownloadAll() {
	fmt.Println("Running update+download cycle...")
	files, _ := filepath.Glob("data/*.json")

	for _, f := range files {
		podcastId := strings.TrimSuffix(filepath.Base(f), ".json")
		updatePodcast(podcastId)
		runDownloadForPodcast(podcastId)
	}
	fmt.Println("Cycle finished")
}

func runDownloadForPodcast(podcastId string) error {
	p, err := loadPodcast(podcastId)
	if err != nil {
		return err
	}

	for i := range p.Episodes {
		ep := p.Episodes[i]
		if ep.DownloadingState.HasToDownload && !ep.DownloadingState.IsDownloading && !ep.DownloadingState.Downloaded {
			p.Episodes[i].DownloadingState.IsDownloading = true
			savePodcastFile(p, podcastId)

			updated, err := downloadEpisode(ep, p.Title)
			if err != nil {
				p.Episodes[i].DownloadingState.IsDownloading = false
			} else {
				p.Episodes[i] = updated
			}
			savePodcastFile(p, podcastId)
		}
	}
	return nil
}

func savePodcastFile(p Podcast, podcastId string) error {
	filePath := filepath.Join("data", fmt.Sprintf("%s.json", podcastId))
	jsonData, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(filePath, jsonData, 0644)
}
