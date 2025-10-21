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

package stepcontroller

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	commonmodels "github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/models"
	"github.com/koderover/zadig/pkg/microservice/aslan/core/common/repository/mongodb"
	"github.com/koderover/zadig/pkg/setting"
	"github.com/koderover/zadig/pkg/shared/client/systemconfig"
	"github.com/koderover/zadig/pkg/tool/git/gitlab"
	"github.com/koderover/zadig/pkg/tool/log"
	"github.com/koderover/zadig/pkg/types/step"
)

type gitCtl struct {
	step    *commonmodels.StepTask
	gitSpec *step.StepGitSpec
	log     *zap.SugaredLogger
}

func NewGitCtl(stepTask *commonmodels.StepTask, log *zap.SugaredLogger) (*gitCtl, error) {
	yamlString, err := yaml.Marshal(stepTask.Spec)
	if err != nil {
		return nil, fmt.Errorf("marshal git spec error: %v", err)
	}
	gitSpec := &step.StepGitSpec{}
	if err := yaml.Unmarshal(yamlString, &gitSpec); err != nil {
		return nil, fmt.Errorf("unmarshal git spec error: %v", err)
	}
	if gitSpec.Proxy == nil {
		gitSpec.Proxy = &step.Proxy{}
	}
	stepTask.Spec = gitSpec
	return &gitCtl{gitSpec: gitSpec, log: log, step: stepTask}, nil
}

func (s *gitCtl) PreRun(ctx context.Context) error {
	for _, repo := range s.gitSpec.Repos {
		cID := repo.CodehostID
		if cID == 0 {
			log.Error("codehostID can't be empty")
			return fmt.Errorf("codehostID can't be empty")
		}
		detail, err := systemconfig.New().GetCodeHost(cID)
		if err != nil {
			s.log.Error(err)
			return err
		}
		repo.Source = detail.Type
		repo.Address = detail.Address
		repo.Username = detail.Username
		repo.Password = detail.Password
		repo.EnableProxy = detail.EnableProxy

		// For GitLab, refresh the OAuth token if expired
		if detail.Type == setting.SourceFromGitlab {
			refreshedToken, err := gitlab.UpdateGitlabToken(cID, detail.AccessToken)
			if err != nil {
				s.log.Warnf("failed to refresh GitLab token for codehost %d: %v, using existing token", cID, err)
				repo.OauthToken = detail.AccessToken
			} else {
				repo.OauthToken = refreshedToken
			}
		} else {
			repo.OauthToken = detail.AccessToken
		}
	}
	proxies, _ := mongodb.NewProxyColl().List(&mongodb.ProxyArgs{})
	if len(proxies) != 0 {
		s.gitSpec.Proxy.Address = proxies[0].Address
		s.gitSpec.Proxy.EnableApplicationProxy = proxies[0].EnableApplicationProxy
		s.gitSpec.Proxy.EnableRepoProxy = proxies[0].EnableRepoProxy
		s.gitSpec.Proxy.NeedPassword = proxies[0].NeedPassword
		s.gitSpec.Proxy.Password = proxies[0].Password
		s.gitSpec.Proxy.Port = proxies[0].Port
		s.gitSpec.Proxy.Type = proxies[0].Type
		s.gitSpec.Proxy.Username = proxies[0].Username
	}
	s.step.Spec = s.gitSpec
	return nil
}

func (s *gitCtl) AfterRun(ctx context.Context) error {
	return nil
}
