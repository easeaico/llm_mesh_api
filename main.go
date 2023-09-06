package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/easeaico/llm_mesh/pkg/llm_mesh"
	"github.com/valyala/fasthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v2"
)

type Config struct {
	HttpServerAddress string `yaml:"http_server_address"`
	MeshServerAddress string `yaml:"mesh_server_address"`
}

func ReadConfigFile(path string) Config {
	var config Config

	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Error reading YAML file: %s", err)
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Fatalf("Error unmarshalling YAML data: %s", err)
	}

	return config
}

type ChatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model            string                  `json:"model"`
	Messages         []ChatCompletionMessage `json:"messages"`
	MaxTokens        int                     `json:"max_tokens,omitempty"`
	Temperature      float32                 `json:"temperature,omitempty"`
	TopP             float32                 `json:"top_p,omitempty"`
	N                int                     `json:"n,omitempty"`
	Stream           bool                    `json:"stream,omitempty"`
	Stop             []string                `json:"stop,omitempty"`
	PresencePenalty  float32                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty float32                 `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int64        `json:"logit_bias,omitempty"`
	User             string                  `json:"user,omitempty"`
}

type chatHandler struct {
	grpcClient llm_mesh.ChatCompletionServiceClient
}

func (h *chatHandler) Handle(ctx *fasthttp.RequestCtx) {
	if ctx.IsOptions() {
		ctx.SetContentType("application/json")
		ctx.SetStatusCode(fasthttp.StatusOK)

		ctx.Response.Header.Set(fasthttp.HeaderAccessControlAllowOrigin, "*")
		ctx.Response.Header.Set(fasthttp.HeaderAccessControlAllowMethods, "POST, OPTIONS")
		ctx.Response.Header.Set(fasthttp.HeaderAccessControlAllowHeaders, "Content-Type, Authorization")
		ctx.Response.Header.Set(fasthttp.HeaderAccessControlMaxAge, "86400")
		return
	}

	if !ctx.IsPost() {
		log.Println("allow methos POST, OPTIONS error")
		ctx.Error("allow methos POST, OPTIONS", fasthttp.StatusMethodNotAllowed)
		return
	}

	chatReq := new(ChatCompletionRequest)
	body := ctx.Request.Body()
	err := json.Unmarshal(body, chatReq)
	if err != nil {
		log.Printf("illegal request body error: %v", err)
		ctx.Error("illegal request body", fasthttp.StatusBadRequest)
		return
	}

	var msgs []*llm_mesh.ChatCompletionMessage
	for _, m := range chatReq.Messages {
		msg := &llm_mesh.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		msgs = append(msgs, msg)
	}

	rpcReq := &llm_mesh.ChatCompletionRequest{
		Model:            chatReq.Model,
		Messages:         msgs,
		MaxTokens:        uint32(chatReq.MaxTokens),
		Temperature:      chatReq.Temperature,
		TopP:             chatReq.TopP,
		N:                uint32(chatReq.N),
		Stream:           chatReq.Stream,
		Stop:             chatReq.Stop,
		PresencePenalty:  chatReq.PresencePenalty,
		FrequencyPenalty: chatReq.FrequencyPenalty,
		LogitBias:        chatReq.LogitBias,
		User:             chatReq.User,
	}

	stream, err := h.grpcClient.ChatCompletion(ctx, rpcReq)
	if err != nil {
		log.Printf("invoke grpc mesh service error: %v", err)
		ctx.Error("illegal request body", fasthttp.StatusBadRequest)
		return
	}

	ctx.Response.Header.Set(fasthttp.HeaderContentType, "text/event-stream")
	ctx.Response.Header.Set(fasthttp.HeaderCacheControl, "no-cache")
	ctx.Response.Header.Set(fasthttp.HeaderConnection, "keep-alive")
	ctx.Response.Header.Set(fasthttp.HeaderTransferEncoding, "chunked")
	ctx.Response.Header.Set(fasthttp.HeaderAccessControlAllowOrigin, "*")
	ctx.Response.Header.Set(fasthttp.HeaderAccessControlAllowHeaders, "Cache-Control")
	ctx.Response.Header.Set(fasthttp.HeaderAccessControlAllowCredentials, "true")
	ctx.SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		for {
			reply, err := stream.Recv()
			if err == io.EOF {
				writeAndFlush(w, "[DONE]")
				return
			}

			if err != nil {
				log.Printf("ChatCompletion(_), %+v", err)
				return
			}

			data, err := json.Marshal(reply)
			if err != nil {
				log.Printf("Marshal error: %v", err)
				return
			}
			writeAndFlush(w, string(data))
		}
	}))

}

func writeAndFlush(w *bufio.Writer, data string) {
	event := fmt.Sprintf("data: %s\n\n", data)
	if _, err := w.Write([]byte(event)); err != nil {
		log.Printf("Error while writting: %v. Closing http connection.", err)
		return
	}

	if err := w.Flush(); err != nil {
		log.Printf("Error while flushing: %v. Closing http connection.", err)
		return
	}
}

func main() {
	conf := ReadConfigFile("config.yaml")
	log.Println(conf)

	conn, err := grpc.Dial(conf.MeshServerAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	grpcClient := llm_mesh.NewChatCompletionServiceClient(conn)
	chatHandler := &chatHandler{
		grpcClient: grpcClient,
	}
	err = fasthttp.ListenAndServe(conf.HttpServerAddress, func(ctx *fasthttp.RequestCtx) {
		log.Printf("http server works: %s\n", string(ctx.Path()))

		switch string(ctx.Path()) {
		case "/v1/chat/completions":
			chatHandler.Handle(ctx)
		default:
			ctx.Error("Unsupported path", fasthttp.StatusNotFound)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
}
