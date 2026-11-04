package xiaohongshu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupChatNameMatches(t *testing.T) {
	cases := []struct {
		display  string
		request  string
		expected bool
	}{
		{"炸猪排小铺", "炸猪排小铺", true},
		{" 炸猪排小铺 ", "炸猪排小铺", true},
		{"Test Group", "test group", true},
		{"炸猪排小铺", "其他群", false},
		{"", "炸猪排小铺", false},
		{"炸猪排小铺", "", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.expected, groupChatNameMatches(c.display, c.request),
			"display=%q request=%q", c.display, c.request)
	}
}

func TestFormatGroupChatNotFoundError(t *testing.T) {
	err := formatGroupChatNotFoundError("缺失群", nil)
	assert.Contains(t, err.Error(), "缺失群")
	assert.Contains(t, err.Error(), groupChatEmptyPlaceholder)

	err = formatGroupChatNotFoundError("缺失群", []string{"群A", "群B"})
	assert.Contains(t, err.Error(), "群A、群B")
	assert.Contains(t, err.Error(), "缺失群")
}

func TestParseGroupNamesFromListJSON(t *testing.T) {
	body := `{"code":0,"success":true,"msg":"成功","data":{"group_list":[{"group_name":"炸猪排小铺","desc":"1人","linkable":true,"group_id":"138175282962938474"}],"admin_group_list":[{"group_name":"管理员群","linkable":false}]}}`
	names, err := parseGroupNamesFromListJSON(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"炸猪排小铺"}, names)

	bodyAdmin := `{"data":{"group_list":[],"admin_group_list":[{"group_name":"运营群","linkable":true}]}}`
	names, err = parseGroupNamesFromListJSON(bodyAdmin)
	require.NoError(t, err)
	assert.Equal(t, []string{"运营群"}, names)
}

func TestCollectGroupNamesFromListPayloadDedup(t *testing.T) {
	data := groupListAPIPayload{
		GroupList: []groupListItem{
			{GroupName: "A", Linkable: true},
			{GroupName: "a", Linkable: true},
		},
		AdminGroupList: []groupListItem{
			{GroupName: "B", Linkable: true},
		},
	}
	assert.Equal(t, []string{"A", "B"}, collectGroupNamesFromListPayload(data))
}
