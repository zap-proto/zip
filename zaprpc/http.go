package zaprpc

import (
	"fmt"

	"github.com/gofiber/fiber/v3"
)

// HTTP header hints. The envelope in the body is canonical; these are a
// fast-path so a router/proxy can route without decoding the body. On
// disagreement the binary envelope wins (see decodeRequest).
const (
	HeaderService = "X-ZAP-Service"
	HeaderMethod  = "X-ZAP-Method"
)

// HTTPHandler returns a fiber.Handler that serves the ZAP RPC plane over
// HTTP POST. The request body is a canonical ZAP envelope (service,
// method, payload); the handler decodes it, dispatches through reg, and
// writes a ZAP response envelope. Returns fiber.Handler (not zip.Handler)
// because zaprpc is imported by zip — depending back would cycle.
//
// Mount it on any zip/Fiber router:
//
//	app.Fiber().Post("/zap", zaprpc.HTTPHandler(app.ZAPRegistry()))
func HTTPHandler(reg *Registry) fiber.Handler {
	return func(c fiber.Ctx) error {
		body := c.Body()
		if len(body) == 0 {
			return writeErr(c, fiber.StatusBadRequest, "empty ZAP body")
		}

		env, err := DecodeEnvelope(body)
		if err != nil {
			return writeErr(c, fiber.StatusBadRequest, err.Error())
		}

		// Headers are only a hint; the envelope is canonical. We accept
		// the header values when the envelope omits them, but never let a
		// header override a present envelope field.
		if env.Service == "" {
			env.Service = c.Get(HeaderService)
		}
		if env.Method == "" {
			env.Method = c.Get(HeaderMethod)
		}
		if env.Service == "" || env.Method == "" {
			return writeErr(c, fiber.StatusBadRequest, "missing service or method")
		}

		out, err := reg.Dispatch(c.Context(), env.Service, env.Method, env.Payload)
		if err != nil {
			if err == ErrNoService {
				return writeErr(c, fiber.StatusNotFound, err.Error())
			}
			return writeErr(c, fiber.StatusInternalServerError, err.Error())
		}

		c.Status(fiber.StatusOK)
		c.Set(fiber.HeaderContentType, "application/zap")
		c.Set(HeaderService, env.Service)
		c.Set(HeaderMethod, env.Method)
		return c.Send(EncodeResponse(out))
	}
}

func writeErr(c fiber.Ctx, status int, msg string) error {
	c.Status(status)
	c.Set(fiber.HeaderContentType, "application/json")
	return c.SendString(fmt.Sprintf(`{"error":%q,"status":%d}`, msg, status))
}
