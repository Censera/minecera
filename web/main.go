package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Root string
}

type Status struct {
	Running       bool   `json:"running"`
	PID           int    `json:"pid"`
	Uptime        string `json:"uptime"`
	CPU           string `json:"cpu"`
	Memory        string `json:"memory"`
	Load          string `json:"load"`
	Disk