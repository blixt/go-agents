package api

import "reflect"

// normalizeNilSlices recursively clones payload values and converts nil slices
// to empty slices so API responses encode [] instead of null.
func normalizeNilSlices(payload any) any {
	if payload == nil {
		return nil
	}
	value := reflect.ValueOf(payload)
	normalized := normalizeValue(value)
	if !normalized.IsValid() {
		return nil
	}
	return normalized.Interface()
}

func normalizeValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(normalizeValue(value.Elem()))
		return out
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		elem := normalizeValue(value.Elem())
		out := reflect.New(value.Type()).Elem()
		if elem.IsValid() {
			out.Set(elem)
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := 0; i < value.NumField(); i++ {
			field := out.Field(i)
			if !field.CanSet() {
				continue
			}
			normalizedField := normalizeValue(value.Field(i))
			assignValue(field, normalizedField, value.Field(i))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.MakeSlice(value.Type(), 0, 0)
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			elem := normalizeValue(value.Index(i))
			assignValue(out.Index(i), elem, value.Index(i))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			elem := normalizeValue(value.Index(i))
			assignValue(out.Index(i), elem, value.Index(i))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key()
			raw := iter.Value()
			normalizedElem := normalizeValue(raw)
			elem := normalizedElem
			if !elem.IsValid() {
				elem = reflect.Zero(value.Type().Elem())
			}
			if !elem.Type().AssignableTo(value.Type().Elem()) {
				if elem.Type().ConvertibleTo(value.Type().Elem()) {
					elem = elem.Convert(value.Type().Elem())
				} else {
					elem = raw
				}
			}
			out.SetMapIndex(key, elem)
		}
		return out
	default:
		return value
	}
}

func assignValue(target, normalized, fallback reflect.Value) {
	if !target.CanSet() {
		return
	}
	if !normalized.IsValid() {
		target.Set(fallback)
		return
	}
	if normalized.Type().AssignableTo(target.Type()) {
		target.Set(normalized)
		return
	}
	if normalized.Type().ConvertibleTo(target.Type()) {
		target.Set(normalized.Convert(target.Type()))
		return
	}
	target.Set(fallback)
}
