// Package activation 实现 /api/openbox/activation(四个方法)和 /api/openbox/tutorial-videos。
// 两者都是前端留下的、后端没有对应概念的接口,按 DESIGN 的 D9 和第 8 节的默认做法处理:
// 激活接口只返回固定的「无需激活」状态,不保存任何东西;教程视频固定返回空列表。
// 响应形状取自前端 api/clientApp.ts 的 OpenboxActivation 和 DESIGN 第 8 节,没有参照上游。
package activation

import (
	"context"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Deps 是本分组的依赖。
type Deps struct {
	// Version 是本后端的版本号,用来拼概览顶栏左边的默认文字「Open-Box <版本>」;空时只显示「Open-Box」。
	Version string
	// KernelVersion 返回 sing-box 内核版本号(如 1.14.4),读不到返回空串;用来拼右边的默认文字。可为 nil。
	KernelVersion func(ctx context.Context) string
}

// state 对应前端的 OpenboxActivation。activated 恒为真(没有激活机制,等于一直解锁),
// left / right 恒为空串(留空 = 概览顶栏显示 defaults)。
type state struct {
	Activated bool     `json:"activated"`
	Left      string   `json:"left"`
	Right     string   `json:"right"`
	Defaults  defaults `json:"defaults"`
}

type defaults struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	// 四个方法都回同一份固定状态:POST(激活码随便给)、PUT(改两边文字)、DELETE(取消激活)都不保存也不报错,
	// 前端据此不会出现「激活码不对」之类的提示。
	fixed := func(w http.ResponseWriter, r *http.Request) {
		httpx.NoStore(w)
		httpx.JSON(w, http.StatusOK, d.state(r.Context()))
	}
	mux.HandleFunc("/api/openbox/activation", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: fixed, http.MethodPost: fixed, http.MethodPut: fixed, http.MethodDelete: fixed,
	}))
	mux.HandleFunc("/api/openbox/tutorial-videos", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, _ *http.Request) {
			httpx.NoStore(w)
			httpx.JSON(w, http.StatusOK, map[string]any{"videos": []any{}, "updatedAt": 0, "staleAfterMs": 86400000})
		},
	}))
}

func (d Deps) state(ctx context.Context) state {
	left := "Open-Box"
	if d.Version != "" {
		left += " " + d.Version
	}
	right := "sing-box"
	if d.KernelVersion != nil {
		if v := d.KernelVersion(ctx); v != "" {
			right += " " + v
		}
	}
	return state{Activated: true, Defaults: defaults{Left: left, Right: right}}
}
