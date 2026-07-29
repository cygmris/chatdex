package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/httpapi"
)

// fakeStore 只做校验 + 记住最后一次生效的值，不落盘——
// 保存到文件那部分由 config.Save 的单测覆盖，这里测的是端点行为。
type fakeStore struct {
	cur    config.Config
	models []string
	note   string
}

func (s *fakeStore) Current() config.Config { return s.cur }

func (s *fakeStore) Apply(c config.Config) []config.FieldError {
	if errs := config.Validate(c); len(errs) > 0 {
		return errs
	}
	s.cur = c
	return nil
}

func (s *fakeStore) Models(context.Context) ([]string, string) { return s.models, s.note }

func newConfigServer(t *testing.T, st *fakeStore) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	(&httpapi.Server{Config: st}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func putJSON(t *testing.T, url string, body any, out any) int {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// 前端整张表单由这个响应渲染，所以它必须覆盖每个字段，且默认值与
// config.Default() 一致——不然界面上的「默认」是假的。
func TestGetConfigCarriesMetaAndDefaults(t *testing.T) {
	srv := newConfigServer(t, &fakeStore{cur: config.Default()})

	var got struct {
		Fields []struct {
			config.FieldMeta
			Value   any `json:"value"`
			Default any `json:"default"`
		} `json:"fields"`
		Path string `json:"path"`
	}
	if code := getJSON(t, srv.URL+"/api/config", &got); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if len(got.Fields) != len(config.Fields()) {
		t.Fatalf("下发 %d 个字段，元信息有 %d 个", len(got.Fields), len(config.Fields()))
	}
	if got.Path == "" {
		t.Error("没告诉前端配置文件在哪")
	}
	def := config.Default()
	for _, f := range got.Fields {
		if f.Label == "" || f.Help == "" || f.Group == "" {
			t.Errorf("%s 缺 label/help/group，界面上会是一格没有说明的输入框", f.Key)
		}
		// JSON 往返后数字都成了 float64，比字符串形式即可
		want := def.Get(f.Key)
		if toS(f.Default) != toS(want) {
			t.Errorf("%s 的默认值 = %v，config.Default() 是 %v", f.Key, f.Default, want)
		}
	}
}

func toS(v any) string { b, _ := json.Marshal(v); return string(b) }

// 界面上能填不等于能存（需求 4.5）。这条不因为「用户自己填的」就放宽。
func TestPutConfigRejectsRemoteLLMEndpoint(t *testing.T) {
	st := &fakeStore{cur: config.Default()}
	srv := newConfigServer(t, st)

	var res struct {
		Errors []config.FieldError `json:"errors"`
	}
	code := putJSON(t, srv.URL+"/api/config",
		map[string]any{"llm": map[string]any{"endpoint": "https://api.openai.com"}}, &res)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d, want 422", code)
	}
	if len(res.Errors) == 0 || res.Errors[0].Key != "llm.endpoint" {
		t.Fatalf("没指出是 llm.endpoint 的问题: %+v", res.Errors)
	}
	if st.cur.LLM.Endpoint != config.Default().LLM.Endpoint {
		t.Error("被拒的取值仍然生效了")
	}
}

// 「保存失败」四个字对使用者没用——他要知道回去改哪一格。
func TestPutConfigPointsAtTheBadField(t *testing.T) {
	srv := newConfigServer(t, &fakeStore{cur: config.Default()})

	var res struct {
		Errors []config.FieldError `json:"errors"`
	}
	code := putJSON(t, srv.URL+"/api/config",
		map[string]any{"summary": map[string]any{"throttle_ms": -1}}, &res)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d, want 422", code)
	}
	var found bool
	for _, e := range res.Errors {
		if e.Key == "summary.throttle_ms" {
			found = true
		}
	}
	if !found {
		t.Errorf("错误里没有 summary.throttle_ms: %+v", res.Errors)
	}
}

// 只回传改动的键也要能存——前端不必把整份配置背回来。
func TestPutConfigAcceptsPartialBody(t *testing.T) {
	st := &fakeStore{cur: config.Default()}
	srv := newConfigServer(t, st)

	if code := putJSON(t, srv.URL+"/api/config",
		map[string]any{"summary": map[string]any{"throttle_ms": 250}}, nil); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if st.cur.Summary.ThrottleMS != 250 {
		t.Errorf("throttle_ms = %d", st.cur.Summary.ThrottleMS)
	}
	if st.cur.Ports.UI != config.Default().Ports.UI {
		t.Errorf("没提到的键被清成零值了: ports.ui = %d", st.cur.Ports.UI)
	}
}

// 本地 LLM 是可选依赖：它不在，设置页其余部分照常可改（需求 4.7）。
func TestModelsDegradeWhenLLMDown(t *testing.T) {
	st := &fakeStore{cur: config.Default(), models: nil, note: "本地 LLM 不可达"}
	srv := newConfigServer(t, st)

	var res struct {
		Models []string `json:"models"`
		Note   string   `json:"note"`
	}
	if code := getJSON(t, srv.URL+"/api/llm/models", &res); code != 200 {
		t.Fatalf("状态码 = %d，LLM 不可用是降级不是错误", code)
	}
	if res.Models == nil {
		t.Error("models 应为空数组而非 null——前端会直接 .map()")
	}
	if res.Note == "" {
		t.Error("空列表没给原因，用户只会看到一个空下拉")
	}
	// 关键：这时候其余配置项仍然能改
	if code := putJSON(t, srv.URL+"/api/config",
		map[string]any{"ui": map[string]any{"light_theme": "paper"}}, nil); code != 200 {
		t.Fatalf("LLM 不可用时改主题也被挡了，状态码 = %d", code)
	}
}
