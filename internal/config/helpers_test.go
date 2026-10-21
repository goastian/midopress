package config

import "reflect"

type reflectV = reflect.Value

func reflectValue(c *Config) reflect.Value { return reflect.ValueOf(c).Elem() }
