package handler

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// AuthHandler is the thin HTTP adapter for public auth endpoints:
// parse → service → respond. It holds no business logic or SQL.
type AuthHandler struct {
	svc *service.AuthService
}

// NewAuthHandler builds an AuthHandler backed by the given service.
func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// RegisterAuthRoutes mounts the public auth routes on the app.
// Named distinctly from handler.RegisterRoutes (the /tasks group) because Go
// forbids two same-name functions in one package regardless of signature.
func RegisterAuthRoutes(app fiber.Router, h *AuthHandler) {
	app.Post("/register", h.Register)
	app.Post("/login", h.Login)
}

// authResponse is the shared success payload for register/login.
type authResponse struct {
	User  *domain.User `json:"user"`
	Token string       `json:"token"`
}

// bindJSON decodes a JSON body into dst, wrapping decode failures as
// ErrValidation so they flow through the P2 pipeline as a 400.
func bindJSON(c fiber.Ctx, dst any) error {
	if err := c.Bind().JSON(dst); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrValidation, err)
	}
	return nil
}

// Register godoc
// @Summary      Register a new user
// @Description  Create an account and return a signed JWT for immediate use.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body domain.RegisterInput true "Registration payload"
// @Success      201 {object} map[string]interface{}
// @Failure      400 {object} ErrorResp
// @Failure      409 {object} ErrorResp
// @Router       /register [post]
func (h *AuthHandler) Register(c fiber.Ctx) error {
	var in domain.RegisterInput
	if err := bindJSON(c, &in); err != nil {
		return err
	}

	user, token, err := h.svc.Register(c.Context(), in)
	if err != nil {
		return err
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"data": authResponse{User: user, Token: token},
	})
}

// Login godoc
// @Summary      Log in
// @Description  Verify credentials and return a signed JWT.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body domain.RegisterInput true "Login payload"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} ErrorResp
// @Failure      401 {object} ErrorResp
// @Router       /login [post]
func (h *AuthHandler) Login(c fiber.Ctx) error {
	var in domain.RegisterInput
	if err := bindJSON(c, &in); err != nil {
		return err
	}

	user, token, err := h.svc.Login(c.Context(), in.Email, in.Password)
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"data": authResponse{User: user, Token: token},
	})
}