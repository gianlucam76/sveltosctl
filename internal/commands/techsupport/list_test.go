/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

package techsupport_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	utilsv1beta1 "github.com/projectsveltos/sveltosctl/api/v1beta1"
	"github.com/projectsveltos/sveltosctl/internal/collector"
	"github.com/projectsveltos/sveltosctl/internal/commands/techsupport"
	"github.com/projectsveltos/sveltosctl/internal/utils"
)

var _ = Describe("Techsupport List", func() {
	BeforeEach(func() {
	})

	It("techsupport list displays all techsupports collected per Techsupport instance", func() {
		techsupportInstance := &utilsv1beta1.Techsupport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: utilsv1beta1.TechsupportSpec{
				Storage: randomString(),
			},
		}

		numOfCollection := 4
		techsupportDir := createTechsupportDirectories(techsupportInstance.Name, techsupportInstance.Spec.Storage,
			numOfCollection)
		techsupportInstance.Spec.Storage = techsupportDir
		By(fmt.Sprintf("Created techsupport instance %s (storage %s)", techsupportInstance.Name, techsupportInstance.Spec.Storage))

		old := os.Stdout // keep backup of the real stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		initObjects := []client.Object{techsupportInstance}

		scheme, err := utils.GetScheme()
		Expect(err).To(BeNil())
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		utils.InitalizeManagementClusterAcces(scheme, nil, nil, c)
		collector.InitializeClient(context.TODO(),
			textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))), c, 10)

		err = techsupport.ListTechsupports(context.TODO(), "",
			textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))
		Expect(err).To(BeNil())

		w.Close()
		var buf bytes.Buffer
		_, err = io.Copy(&buf, r)
		Expect(err).To(BeNil())
		/*
		  // Following is an example of techsupport list
		   +-----------------+---------------------+
		   | SNAPSHOT POLICY |        DATE         |
		   +-----------------+---------------------+
		   | daily           | 2022-10-07:01:10:59 |
		   | daily           | 2022-10-07:02:10:56 |
		   +-----------------+---------------------+
		*/

		foundCollection := 0
		lines := strings.Split(buf.String(), "\n")
		for i := range lines {
			if strings.Contains(lines[i], techsupportInstance.Name) {
				foundCollection++
			}
		}

		Expect(foundCollection).To(Equal(numOfCollection))

		os.Stdout = old
	})
})
