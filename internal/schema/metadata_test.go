package schema

import "testing"

func TestMetaVisibleToConsumer(t *testing.T) {
	tests := []struct {
		name           string
		meta           map[string]any
		consumer       string
		defaultVisible bool
		want           bool
	}{
		{
			name:           "default true without rules",
			meta:           nil,
			consumer:       ConsumerAgentContext,
			defaultVisible: true,
			want:           true,
		},
		{
			name: "exclude list hides consumer",
			meta: map[string]any{
				MetaDeliveryExclude: []string{ConsumerAgentContext},
			},
			consumer:       ConsumerAgentContext,
			defaultVisible: true,
			want:           false,
		},
		{
			name: "include list enables consumer",
			meta: map[string]any{
				MetaDeliveryInclude: []any{ConsumerAgentContext},
				MetaDeliveryExclude: []string{ConsumerAgentContext},
			},
			consumer:       ConsumerAgentContext,
			defaultVisible: false,
			want:           true,
		},
		{
			name: "opt in blocks by default",
			meta: map[string]any{
				MetaDeliveryMode: DeliveryModeOptIn,
			},
			consumer:       ConsumerAgentContext,
			defaultVisible: true,
			want:           false,
		},
		{
			name: "opt in with include allows",
			meta: map[string]any{
				MetaDeliveryMode:    DeliveryModeOptIn,
				MetaDeliveryInclude: "agent_context, telemetry",
			},
			consumer:       ConsumerAgentContext,
			defaultVisible: false,
			want:           true,
		},
		{
			name: "opt out enables by default",
			meta: map[string]any{
				MetaDeliveryMode: DeliveryModeOptOut,
			},
			consumer:       ConsumerAgentContext,
			defaultVisible: false,
			want:           true,
		},
		{
			name: "wildcard exclude hides all",
			meta: map[string]any{
				MetaDeliveryExclude: "*",
			},
			consumer:       ConsumerAgentContext,
			defaultVisible: true,
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetaVisibleToConsumer(tt.meta, tt.consumer, tt.defaultVisible); got != tt.want {
				t.Fatalf("MetaVisibleToConsumer() = %v, want %v", got, tt.want)
			}
		})
	}
}
