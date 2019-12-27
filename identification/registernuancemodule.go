// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build with_nuance_module

package identification

import (
	"aos_servicemanager/database"
	"aos_servicemanager/identification/nuancemodule"
)

func init() {
	Register(nuancemodule.Name, func(configJSON []byte, db *database.Database) (module Module, err error) {
		return nuancemodule.New(configJSON)
	})
}
