package handlers

import (
	"net/http"

	// Updated import path
	"course-allocator/internal/repository"

	"github.com/gin-gonic/gin"
)

type CourseHandler struct {
	repo *repository.Repo
}

func NewCourseHandler(repo *repository.Repo) *CourseHandler {
	return &CourseHandler{repo: repo}
}

func (h *CourseHandler) ResetCourses(c *gin.Context) {
	if err := h.repo.ResetCourses(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset course availability"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Course seats reset to 100"})
}

// Phase 1: Vulnerable Endpoint (Simulates the Race Condition)
func (h *CourseHandler) RegisterVulnerable(c *gin.Context) {
	success, err := h.repo.RegisterCourseVulnerable()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal system error"})
		return
	}

	if success {
		c.JSON(http.StatusOK, gin.H{"message": "Registered (Vulnerable)!"})
	} else {
		c.JSON(http.StatusGone, gin.H{"message": "Course Full!"})
	}
}

// Phase 2: Atomic Endpoint (Safe from Race Conditions with Dynamic Pricing)
func (h *CourseHandler) RegisterCourse(c *gin.Context) {
	// 1. Dynamic Pricing Engine: Read from high-speed cache
	availableSeats, err := h.repo.GetAvailableSeats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch seat availability"})
		return
	}

	// Calculate credit requirement based on demand
	creditsRequired := 10
	if availableSeats <= 50 {
		creditsRequired = 15 // Surge pricing triggers when 50% of seats are gone
	}

	// 2. Execute Atomic Registration Lock
	success, err := h.repo.RegisterCourseAtomic()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal system error"})
		return
	}

	// 3. Return Contextual Response
	if success {
		c.JSON(http.StatusOK, gin.H{
			"message":         "Successfully registered for the course!",
			"credits_charged": creditsRequired,
		})
	} else {
		c.JSON(http.StatusGone, gin.H{"message": "Course registration is closed (Full)!"})
	}
}
