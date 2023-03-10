/*
Copyright 2022 The KodeRover Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package instantmessage

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	configbase "github.com/koderover/zadig/pkg/config"
	"github.com/koderover/zadig/pkg/microservice/aslan/config"
	"github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/models"
	"github.com/koderover/zadig/pkg/tool/log"
	"github.com/koderover/zadig/pkg/types"
	"github.com/koderover/zadig/pkg/types/step"
)

func (w *Service) SendWorkflowTaskAproveNotifications(workflowName string, taskID int64) error {
	resp, err := w.workflowV4Coll.Find(workflowName)
	if err != nil {
		errMsg := fmt.Sprintf("failed to find workflowv4, err: %s", err)
		log.Error(errMsg)
		return errors.New(errMsg)
	}
	task, err := w.workflowTaskV4Coll.Find(workflowName, taskID)
	if err != nil {
		errMsg := fmt.Sprintf("failed to find workflowv4 task, err: %s", err)
		log.Error(errMsg)
		return errors.New(errMsg)
	}
	for _, notify := range resp.NotifyCtls {
		statusSets := sets.NewString(notify.NotifyTypes...)
		if !statusSets.Has(string(config.StatusWaitingApprove)) {
			continue
		}
		if !notify.Enabled {
			continue
		}
		title, content, larkCard, err := w.getApproveNotificationContent(notify, task)
		if err != nil {
			errMsg := fmt.Sprintf("failed to get notification content, err: %s", err)
			log.Error(errMsg)
			return errors.New(errMsg)
		}
		if err := w.sendNotification(title, content, notify, larkCard); err != nil {
			log.Errorf("failed to send notification, err: %s", err)
		}
	}
	return nil
}

func (w *Service) SendWorkflowTaskNotifications(task *models.WorkflowTask) error {
	resp, err := w.workflowV4Coll.Find(task.WorkflowName)
	if err != nil {
		errMsg := fmt.Sprintf("failed to find workflowv4, err: %s", err)
		log.Error(errMsg)
		return errors.New(errMsg)
	}
	if len(resp.NotifyCtls) == 0 {
		return nil
	}
	if task.TaskID <= 0 {
		return nil
	}
	statusChanged := false
	preTask, err := w.workflowTaskV4Coll.Find(task.WorkflowName, task.TaskID-1)
	if err != nil {
		errMsg := fmt.Sprintf("failed to find previous workflowv4, err: %s", err)
		log.Error(errMsg)
		statusChanged = true
	}
	if preTask != nil && task.Status != preTask.Status && task.Status != config.StatusRunning {
		statusChanged = true
	}
	if task.Status == config.StatusCreated {
		statusChanged = false
	}
	for _, notify := range resp.NotifyCtls {
		if !notify.Enabled {
			continue
		}
		statusSets := sets.NewString(notify.NotifyTypes...)
		if statusSets.Has(string(task.Status)) || (statusChanged && statusSets.Has(string(config.StatusChanged))) {
			title, content, larkCard, err := w.getNotificationContent(notify, task)
			if err != nil {
				errMsg := fmt.Sprintf("failed to get notification content, err: %s", err)
				log.Error(errMsg)
				return errors.New(errMsg)
			}
			if err := w.sendNotification(title, content, notify, larkCard); err != nil {
				log.Errorf("failed to send notification, err: %s", err)
			}
		}
	}
	return nil
}
func (w *Service) getApproveNotificationContent(notify *models.NotifyCtl, task *models.WorkflowTask) (string, string, *LarkCard, error) {
	workflowNotification := &workflowTaskNotification{
		Task:               task,
		EncodedDisplayName: url.PathEscape(task.WorkflowDisplayName),
		BaseURI:            configbase.SystemAddress(),
		WebHookType:        notify.WebHookType,
		TotalTime:          time.Now().Unix() - task.StartTime,
	}

	tplTitle := "{{if ne .WebHookType \"feishu\"}}#### {{end}}{{getIcon .Task.Status }}{{if eq .WebHookType \"wechat\"}}<font color=\"markdownColorInfo\">?????????{{.Task.WorkflowDisplayName}} #{{.Task.TaskID}} ????????????</font>{{else}}????????? {{.Task.WorkflowDisplayName}} #{{.Task.TaskID}} ????????????{{end}} \n"
	tplBaseInfo := []string{"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{.Task.TaskCreator}} \n",
		"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{.Task.ProjectName}} \n",
		"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{ getStartTime .Task.StartTime}} \n",
		"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{ getDuration .TotalTime}} \n",
	}
	title, err := getWorkflowTaskTplExec(tplTitle, workflowNotification)
	if err != nil {
		return "", "", nil, err
	}
	buttonContent := "????????????????????????"
	workflowDetailURL := "{{.BaseURI}}/v1/projects/detail/{{.Task.ProjectName}}/pipelines/custom/{{.Task.WorkflowName}}/{{.Task.TaskID}}?display_name={{.EncodedDisplayName}}"
	moreInformation := fmt.Sprintf("[%s](%s)", buttonContent, workflowDetailURL)
	if notify.WebHookType != feiShuType {
		tplcontent := strings.Join(tplBaseInfo, "")
		tplcontent = tplcontent + getNotifyAtContent(notify)
		tplcontent = fmt.Sprintf("%s%s%s", title, tplcontent, moreInformation)
		content, err := getWorkflowTaskTplExec(tplcontent, workflowNotification)
		if err != nil {
			return "", "", nil, err
		}
		return title, content, nil, nil
	}

	lc := NewLarkCard()
	lc.SetConfig(true)
	lc.SetHeader(feishuHeaderTemplateGreen, title, feiShuTagText)
	for idx, feildContent := range tplBaseInfo {
		feildExecContent, _ := getWorkflowTaskTplExec(feildContent, workflowNotification)
		lc.AddI18NElementsZhcnFeild(feildExecContent, idx == 0)
	}
	workflowDetailURL, _ = getWorkflowTaskTplExec(workflowDetailURL, workflowNotification)
	lc.AddI18NElementsZhcnAction(buttonContent, workflowDetailURL)
	return "", "", lc, nil
}

func (w *Service) getNotificationContent(notify *models.NotifyCtl, task *models.WorkflowTask) (string, string, *LarkCard, error) {
	workflowNotification := &workflowTaskNotification{
		Task:               task,
		EncodedDisplayName: url.PathEscape(task.WorkflowDisplayName),
		BaseURI:            configbase.SystemAddress(),
		WebHookType:        notify.WebHookType,
		TotalTime:          time.Now().Unix() - task.StartTime,
	}

	tplTitle := "{{if ne .WebHookType \"feishu\"}}#### {{end}}{{getIcon .Task.Status }}{{if eq .WebHookType \"wechat\"}}<font color=\"{{ getColor .Task.Status }}\">?????????{{.Task.WorkflowDisplayName}} #{{.Task.TaskID}} {{ taskStatus .Task.Status }}</font>{{else}}????????? {{.Task.WorkflowDisplayName}} #{{.Task.TaskID}} {{ taskStatus .Task.Status }}{{end}} \n"
	tplBaseInfo := []string{"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{.Task.TaskCreator}} \n",
		"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{.Task.ProjectName}} \n",
		"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{ getStartTime .Task.StartTime}} \n",
		"{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???{{ getDuration .TotalTime}} \n",
	}
	jobContents := []string{}
	for _, stage := range task.Stages {
		for _, job := range stage.Jobs {
			jobTplcontent := "\n\n{{if eq .WebHookType \"dingding\"}}---\n\n##### {{end}}**{{jobType .Job.JobType }}**: {{.Job.Name}}    **??????**: {{taskStatus .Job.Status }} \n"
			switch job.JobType {
			case string(config.JobZadigBuild):
				fallthrough
			case string(config.JobFreestyle):
				jobSpec := &models.JobTaskFreestyleSpec{}
				models.IToi(job.Spec, jobSpec)
				repos := []*types.Repository{}
				for _, stepTask := range jobSpec.Steps {
					if stepTask.StepType == config.StepGit {
						stepSpec := &step.StepGitSpec{}
						models.IToi(stepTask.Spec, stepSpec)
						repos = stepSpec.Repos
					}
				}
				branchTag, commitID, gitCommitURL := "", "", ""
				commitMsgs := []string{}
				var prInfoList []string
				var prInfo string
				for idx, buildRepo := range repos {
					if idx == 0 || buildRepo.IsPrimary {
						branchTag = buildRepo.Branch
						if buildRepo.Tag != "" {
							branchTag = buildRepo.Tag
						}
						if len(buildRepo.CommitID) > 8 {
							commitID = buildRepo.CommitID[0:8]
						}
						var prLinkBuilder func(baseURL, owner, repoName string, prID int) string
						switch buildRepo.Source {
						case types.ProviderGithub:
							prLinkBuilder = func(baseURL, owner, repoName string, prID int) string {
								return fmt.Sprintf("%s/%s/%s/pull/%d", baseURL, owner, repoName, prID)
							}
						case types.ProviderGitee:
							prLinkBuilder = func(baseURL, owner, repoName string, prID int) string {
								return fmt.Sprintf("%s/%s/%s/pulls/%d", baseURL, owner, repoName, prID)
							}
						case types.ProviderGitlab:
							prLinkBuilder = func(baseURL, owner, repoName string, prID int) string {
								return fmt.Sprintf("%s/%s/%s/merge_requests/%d", baseURL, owner, repoName, prID)
							}
						case types.ProviderGerrit:
							prLinkBuilder = func(baseURL, owner, repoName string, prID int) string {
								return fmt.Sprintf("%s/%d", baseURL, prID)
							}
						}
						prInfoList = []string{}
						sort.Ints(buildRepo.PRs)
						for _, id := range buildRepo.PRs {
							link := prLinkBuilder(buildRepo.Address, buildRepo.RepoOwner, buildRepo.RepoName, id)
							prInfoList = append(prInfoList, fmt.Sprintf("[#%d](%s)", id, link))
						}
						commitMsg := strings.Trim(buildRepo.CommitMessage, "\n")
						commitMsgs = strings.Split(commitMsg, "\n")
						gitCommitURL = fmt.Sprintf("%s/%s/%s/commit/%s", buildRepo.Address, buildRepo.RepoOwner, buildRepo.RepoName, commitID)
					}
				}
				if len(prInfoList) != 0 {
					// need an extra space at the end
					prInfo = strings.Join(prInfoList, " ") + " "
				}
				image := ""
				for _, env := range jobSpec.Properties.Envs {
					if env.Key == "IMAGE" {
						image = env.Value
					}
				}
				if len(commitID) > 0 {
					jobTplcontent += fmt.Sprintf("{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???%s %s[%s](%s) \n", branchTag, prInfo, commitID, gitCommitURL)
					jobTplcontent += "{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???"
					if len(commitMsgs) == 1 {
						jobTplcontent += fmt.Sprintf("%s \n", commitMsgs[0])
					} else {
						jobTplcontent += "\n"
						for _, commitMsg := range commitMsgs {
							jobTplcontent += fmt.Sprintf("%s \n", commitMsg)
						}
					}
				}
				if image != "" {
					jobTplcontent += fmt.Sprintf("{{if eq .WebHookType \"dingding\"}}##### {{end}}**????????????**???%s \n", image)
				}
			case string(config.JobZadigDeploy):
				jobSpec := &models.JobTaskDeploySpec{}
				models.IToi(job.Spec, jobSpec)
				jobTplcontent += fmt.Sprintf("{{if eq .WebHookType \"dingding\"}}##### {{end}}**??????**???%s \n", jobSpec.Env)
			case string(config.JobZadigHelmDeploy):
				jobSpec := &models.JobTaskHelmDeploySpec{}
				models.IToi(job.Spec, jobSpec)
				jobTplcontent += fmt.Sprintf("{{if eq .WebHookType \"dingding\"}}##### {{end}}**??????**???%s \n", jobSpec.Env)
			}
			jobNotifaication := &jobTaskNotification{
				Job:         job,
				WebHookType: notify.WebHookType,
			}

			jobContent, err := getJobTaskTplExec(jobTplcontent, jobNotifaication)
			if err != nil {
				return "", "", nil, err
			}
			jobContents = append(jobContents, jobContent)
		}
	}
	title, err := getWorkflowTaskTplExec(tplTitle, workflowNotification)
	if err != nil {
		return "", "", nil, err
	}
	buttonContent := "????????????????????????"
	workflowDetailURL := "{{.BaseURI}}/v1/projects/detail/{{.Task.ProjectName}}/pipelines/custom/{{.Task.WorkflowName}}/{{.Task.TaskID}}?display_name={{.EncodedDisplayName}}"
	moreInformation := fmt.Sprintf("\n\n{{if eq .WebHookType \"dingding\"}}---\n\n{{end}}[%s](%s)", buttonContent, workflowDetailURL)
	if notify.WebHookType != feiShuType {
		tplcontent := strings.Join(tplBaseInfo, "")
		tplcontent += strings.Join(jobContents, "")
		tplcontent = tplcontent + getNotifyAtContent(notify)
		tplcontent = fmt.Sprintf("%s%s%s", title, tplcontent, moreInformation)
		content, err := getWorkflowTaskTplExec(tplcontent, workflowNotification)
		if err != nil {
			return "", "", nil, err
		}
		return title, content, nil, nil
	}

	lc := NewLarkCard()
	lc.SetConfig(true)
	lc.SetHeader(getColorTemplateWithStatus(task.Status), title, feiShuTagText)
	for idx, feildContent := range tplBaseInfo {
		feildExecContent, _ := getWorkflowTaskTplExec(feildContent, workflowNotification)
		lc.AddI18NElementsZhcnFeild(feildExecContent, idx == 0)
	}
	for _, feildContent := range jobContents {
		feildExecContent, _ := getWorkflowTaskTplExec(feildContent, workflowNotification)
		lc.AddI18NElementsZhcnFeild(feildExecContent, true)
	}
	workflowDetailURL, _ = getWorkflowTaskTplExec(workflowDetailURL, workflowNotification)
	lc.AddI18NElementsZhcnAction(buttonContent, workflowDetailURL)
	return "", "", lc, nil
}

type workflowTaskNotification struct {
	Task               *models.WorkflowTask `json:"task"`
	EncodedDisplayName string               `json:"encoded_display_name"`
	BaseURI            string               `json:"base_uri"`
	WebHookType        string               `json:"web_hook_type"`
	TotalTime          int64                `json:"total_time"`
}

func getWorkflowTaskTplExec(tplcontent string, args *workflowTaskNotification) (string, error) {
	tmpl := template.Must(template.New("notify").Funcs(template.FuncMap{
		"getColor": func(status config.Status) string {
			if status == config.StatusPassed || status == config.StatusCreated {
				return markdownColorInfo
			} else if status == config.StatusTimeout || status == config.StatusCancelled {
				return markdownColorComment
			} else if status == config.StatusFailed {
				return markdownColorWarning
			}
			return markdownColorComment
		},
		"taskStatus": func(status config.Status) string {
			if status == config.StatusPassed {
				return "????????????"
			} else if status == config.StatusCancelled {
				return "????????????"
			} else if status == config.StatusTimeout {
				return "????????????"
			} else if status == config.StatusReject {
				return "???????????????"
			} else if status == config.StatusCreated {
				return "????????????"
			}
			return "????????????"
		},
		"getIcon": func(status config.Status) string {
			if status == config.StatusPassed || status == config.StatusCreated {
				return "????"
			}
			return "??????"
		},
		"getStartTime": func(startTime int64) string {
			return time.Unix(startTime, 0).Format("2006-01-02 15:04:05")
		},
		"getDuration": func(startTime int64) string {
			duration, er := time.ParseDuration(strconv.FormatInt(startTime, 10) + "s")
			if er != nil {
				log.Errorf("getTplExec ParseDuration err:%s", er)
				return "0s"
			}
			return duration.String()
		},
	}).Parse(tplcontent))

	buffer := bytes.NewBufferString("")
	if err := tmpl.Execute(buffer, args); err != nil {
		log.Errorf("getTplExec Execute err:%s", err)
		return "", fmt.Errorf("getTplExec Execute err:%s", err)

	}
	return buffer.String(), nil
}

type jobTaskNotification struct {
	Job         *models.JobTask `json:"task"`
	WebHookType string          `json:"web_hook_type"`
}

func getJobTaskTplExec(tplcontent string, args *jobTaskNotification) (string, error) {
	tmpl := template.Must(template.New("notify").Funcs(template.FuncMap{
		"taskStatus": func(status config.Status) string {
			if status == config.StatusPassed {
				return "????????????"
			} else if status == config.StatusCancelled {
				return "????????????"
			} else if status == config.StatusTimeout {
				return "????????????"
			} else if status == config.StatusReject {
				return "???????????????"
			} else if status == "" {
				return "?????????"
			}
			return "????????????"
		},
		"jobType": func(jobType string) string {
			switch jobType {
			case string(config.JobZadigBuild):
				return "??????"
			case string(config.JobZadigDeploy):
				return "??????"
			case string(config.JobZadigHelmDeploy):
				return "helm??????"
			case string(config.JobCustomDeploy):
				return "???????????????"
			case string(config.JobFreestyle):
				return "????????????"
			case string(config.JobPlugin):
				return "???????????????"
			case string(config.JobZadigTesting):
				return "??????"
			case string(config.JobZadigScanning):
				return "????????????"
			case string(config.JobZadigDistributeImage):
				return "????????????"
			case string(config.JobK8sBlueGreenDeploy):
				return "????????????"
			case string(config.JobK8sBlueGreenRelease):
				return "????????????"
			case string(config.JobK8sCanaryDeploy):
				return "???????????????"
			case string(config.JobK8sCanaryRelease):
				return "???????????????"
			case string(config.JobK8sGrayRelease):
				return "????????????"
			case string(config.JobK8sGrayRollback):
				return "????????????"
			case string(config.JobK8sPatch):
				return "?????? k8s YAML"
			case string(config.JobIstioRelease):
				return "istio ??????"
			case string(config.JobIstioRollback):
				return "istio ??????"
			case string(config.JobJira):
				return "jira ??????????????????"
			case string(config.JobNacos):
				return "Nacos ????????????"
			case string(config.JobApollo):
				return "Apollo ????????????"
			case string(config.JobMeegoTransition):
				return "???????????????????????????"
			default:
				return string(jobType)
			}
		},
	}).Parse(tplcontent))

	buffer := bytes.NewBufferString("")
	if err := tmpl.Execute(buffer, args); err != nil {
		log.Errorf("getTplExec Execute err:%s", err)
		return "", fmt.Errorf("getTplExec Execute err:%s", err)

	}
	return buffer.String(), nil
}

func (w *Service) sendNotification(title, content string, notify *models.NotifyCtl, card *LarkCard) error {
	switch notify.WebHookType {
	case dingDingType:
		if err := w.sendDingDingMessage(notify.DingDingWebHook, title, content, notify.AtMobiles); err != nil {
			return err
		}
	case feiShuType:
		if err := w.sendFeishuMessage(notify.FeiShuWebHook, card); err != nil {
			return err
		}
		if err := w.sendFeishuMessageOfSingleType("", notify.FeiShuWebHook, getNotifyAtContent(notify)); err != nil {
			return err
		}
	default:
		if err := w.SendWeChatWorkMessage(weChatTextTypeMarkdown, notify.WeChatWebHook, content); err != nil {
			return err
		}
	}
	return nil
}
