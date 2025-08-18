package service

import (
	"reflect"
	"strings"
)

func ToMap(c any) map[string]string {
	m := make(map[string]string)
	v := reflect.ValueOf(c)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		s := strings.SplitN(tag, ",", 2)[0]
		m[s] = v.Field(i).String()
	}
	return m
}

func FromMap(c any, m map[string]string) {
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		s := strings.SplitN(tag, ",", 2)[0]
		if val, ok := m[s]; ok {
			v.Field(i).SetString(val)
		}
	}
}
