package sessionclassification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	feature "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
)

const maxJevResponseBytes = 64 << 10

type JevDecider struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
}

func NewJevDecider(cfg feature.RemoteConfig)(*JevDecider,error){
	key:=strings.TrimSpace(os.Getenv(cfg.APIKeyEnv));if key==""{return nil,fmt.Errorf("session-classification: environment %s is empty",cfg.APIKeyEnv)}
	u,err:=url.Parse(cfg.Endpoint);if err!=nil||u.Host==""{return nil,fmt.Errorf("session-classification: invalid Jev endpoint")}
	if u.Scheme!="https" && !(u.Scheme=="http"&&isLoopbackHost(u.Hostname())){return nil,fmt.Errorf("session-classification: Jev endpoint requires https")}
	cl:=&http.Client{Timeout:cfg.Timeout,CheckRedirect:func(req *http.Request,via []*http.Request)error{return http.ErrUseLastResponse}}
	return &JevDecider{endpoint:u.String(),model:cfg.Model,apiKey:key,client:cl},nil
}
func (d *JevDecider) Decide(ctx context.Context,in feature.RemoteInput)(feature.RemoteDecision,error){
	state:=map[string]any{
		"operation":in.Operation,
		"client_family":in.ClientFamily,
		"ambiguous_client":in.HasAmbiguousClient,
		"tool_categories":uint16(in.ToolCategories),
		"workspace_class":in.WorkspaceClass,
		"local_evidence_code":in.LocalEvidenceCode,
	}
	body:=map[string]any{"model":d.model,"state":state,"questions":map[string]any{
		"coding_agent":map[string]any{"type":"noul","instructions":"Is this bounded client/tool/workspace evidence characteristic of an interactive software coding agent session?","criteria":map[string]any{
			"true":"A coding-agent harness or a coding-oriented structured tool capability cluster is present.",
			"false":"The evidence is generic, ambiguous, browser-only, or not characteristic of a coding agent.",
		}},
	}}
	b,err:=json.Marshal(body);if err!=nil{return feature.RemoteDecision{},err}
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,d.endpoint,bytes.NewReader(b));if err!=nil{return feature.RemoteDecision{},err}
	req.Header.Set("Authorization","Bearer "+d.apiKey);req.Header.Set("Content-Type","application/json")
	resp,err:=d.client.Do(req);if err!=nil{return feature.RemoteDecision{},err};defer resp.Body.Close()
	if resp.StatusCode<200||resp.StatusCode>=300{return feature.RemoteDecision{},fmt.Errorf("session-classification: Jev HTTP status %d",resp.StatusCode)}
	raw,err:=io.ReadAll(io.LimitReader(resp.Body,maxJevResponseBytes+1));if err!=nil{return feature.RemoteDecision{},err};if len(raw)>maxJevResponseBytes{return feature.RemoteDecision{},fmt.Errorf("session-classification: Jev response too large")}
	var out struct{Answers map[string]struct{Type string `json:"type"`; Noul float64 `json:"noul"`} `json:"answers"`}
	if err:=json.Unmarshal(raw,&out);err!=nil{return feature.RemoteDecision{},fmt.Errorf("session-classification: decode Jev response: %w",err)}
	a,ok:=out.Answers["coding_agent"];if !ok||a.Type!="noul"||a.Noul<0||a.Noul>1{return feature.RemoteDecision{},fmt.Errorf("session-classification: invalid Jev coding_agent answer")}
	return feature.RemoteDecision{CodingProbability:a.Noul},nil
}
func isLoopbackHost(host string)bool{ip:=net.ParseIP(host);return strings.EqualFold(host,"localhost")||(ip!=nil&&ip.IsLoopback())}
