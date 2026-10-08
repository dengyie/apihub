package service

import (
	"github.com/dengyie/apihub/setting/operation_setting"
	"github.com/dengyie/apihub/setting/system_setting"
)

func GetCallbackAddress() string {
	if operation_setting.CustomCallbackAddress == "" {
		return system_setting.ServerAddress
	}
	return operation_setting.CustomCallbackAddress
}
