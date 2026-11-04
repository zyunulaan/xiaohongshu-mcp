package xiaohongshu

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const (
	groupChatDropdownTimeout  = 10 * time.Second
	groupChatDropdownPoll     = 200 * time.Millisecond
	groupChatPlaceholder      = "选择群聊"
	groupChatEmptyPlaceholder = "暂无群聊"
)

// groupListAPIPayload 对应 edith group_list 接口 data 字段（仅用于解析可选群聊名）。
type groupListAPIPayload struct {
	GroupList      []groupListItem `json:"group_list"`
	AdminGroupList []groupListItem `json:"admin_group_list"`
}

type groupListItem struct {
	GroupName string `json:"group_name"`
	Linkable  bool   `json:"linkable"`
}

func groupChatNameMatches(displayName, requested string) bool {
	displayName = strings.TrimSpace(displayName)
	requested = strings.TrimSpace(requested)
	return displayName != "" && requested != "" &&
		strings.EqualFold(displayName, requested)
}

func formatGroupChatNotFoundError(requested string, available []string) error {
	requested = strings.TrimSpace(requested)
	if len(available) == 0 {
		return errors.Errorf("无法关联群聊 %q：当前没有可选择的群聊（%s）", requested, groupChatEmptyPlaceholder)
	}
	return errors.Errorf("无法关联群聊 %q：未找到匹配项。可选群聊名称: %s", requested, strings.Join(available, "、"))
}

// parseGroupNamesFromListJSON 从 group_list 接口响应体提取可展示的群聊名称。
func parseGroupNamesFromListJSON(body string) ([]string, error) {
	var resp struct {
		Data groupListAPIPayload `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	return collectGroupNamesFromListPayload(resp.Data), nil
}

func collectGroupNamesFromListPayload(data groupListAPIPayload) []string {
	var names []string
	seen := make(map[string]struct{})
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	for _, item := range data.GroupList {
		if item.Linkable {
			appendName(item.GroupName)
		}
	}
	for _, item := range data.AdminGroupList {
		if item.Linkable {
			appendName(item.GroupName)
		}
	}
	return names
}

// setGroupChat 在发布页「添加组件」里选择群聊；groupChat 为空则跳过。
func setGroupChat(page *rod.Page, groupChat string) error {
	groupChat = strings.TrimSpace(groupChat)
	if groupChat == "" {
		return nil
	}

	closeFeatureGuide(page)

	wrapper, err := page.Element(".group-card-wrapper")
	if err != nil {
		return errors.Wrap(formatGroupChatNotFoundError(groupChat, nil), "未找到群聊选择组件")
	}

	if err := clickGroupChatSelect(page, wrapper); err != nil {
		return errors.Wrap(err, "打开群聊下拉框失败")
	}

	available, empty, err := waitGroupChatOptions(page)
	if err != nil {
		return err
	}
	if empty {
		return formatGroupChatNotFoundError(groupChat, nil)
	}

	target, err := findGroupChatOption(page, groupChat)
	if err != nil {
		return err
	}
	if target == nil {
		return formatGroupChatNotFoundError(groupChat, available)
	}

	if err := humanize.Click(target); err != nil {
		return errors.Wrapf(formatGroupChatNotFoundError(groupChat, available), "点击群聊选项失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := verifySelectedGroupChat(wrapper, groupChat); err != nil {
		return errors.Wrap(formatGroupChatNotFoundError(groupChat, available), err.Error())
	}

	logrus.Infof("已关联群聊: %s", groupChat)
	return nil
}

func clickGroupChatSelect(page *rod.Page, wrapper *rod.Element) error {
	selectMain, err := wrapper.Element(".d-select-main")
	if err != nil {
		content, err2 := wrapper.Element(".d-select-content")
		if err2 != nil {
			return errors.Wrap(err, "查找群聊下拉触发区域失败")
		}
		selectMain = content
	}

	if err := selectMain.ScrollIntoView(); err != nil {
		return errors.Wrap(err, "滚动群聊选择框到可视区域失败")
	}
	time.Sleep(200 * time.Millisecond)

	shape, err := selectMain.Shape()
	if err != nil {
		return errors.Wrap(err, "获取群聊下拉框位置失败")
	}
	if len(shape.Quads) == 0 {
		return errors.New("群聊下拉框无可点击区域")
	}

	quad := shape.Quads[0]
	minX, maxX := quad[0], quad[0]
	minY, maxY := quad[1], quad[1]
	for i := 0; i < quad.Len(); i++ {
		x := quad[i*2]
		y := quad[i*2+1]
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}

	x := minX + (maxX-minX)/2
	y := minY + (maxY-minY)/2
	if err := humanize.ClickAt(page, proto.Point{X: x, Y: y}); err != nil {
		return errors.Wrap(err, "点击群聊下拉框失败")
	}
	return nil
}

func waitGroupChatOptions(page *rod.Page) (available []string, empty bool, err error) {
	deadline := time.Now().Add(groupChatDropdownTimeout)
	for time.Now().Before(deadline) {
		available, empty, found, collectErr := collectGroupChatOptions(page)
		if collectErr != nil {
			return available, empty, collectErr
		}
		if found {
			return available, empty, nil
		}
		time.Sleep(groupChatDropdownPoll)
	}
	return nil, false, errors.New("等待群聊列表超时")
}

func collectGroupChatOptions(page *rod.Page) (available []string, empty bool, found bool, err error) {
	popovers, err := page.Elements(".d-popover.d-dropdown")
	if err != nil {
		return nil, false, false, err
	}

	for _, pop := range popovers {
		visible, err := pop.Visible()
		if err != nil || !visible {
			continue
		}

		text, err := pop.Text()
		if err != nil {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if strings.Contains(text, groupChatEmptyPlaceholder) {
			return nil, true, true, nil
		}

		names, err := readGroupChatOptionNames(pop)
		if err != nil {
			return nil, false, false, err
		}
		if len(names) > 0 {
			return names, false, true, nil
		}
	}

	return nil, false, false, nil
}

func readGroupChatOptionNames(popover *rod.Element) ([]string, error) {
	options, err := popover.Elements("div.item.custom-option")
	if err != nil {
		return nil, err
	}

	var names []string
	for _, opt := range options {
		nameElem, err := opt.Element(".group-info .name")
		if err != nil {
			continue
		}
		name, err := nameElem.Text()
		if err != nil {
			continue
		}
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func findGroupChatOption(page *rod.Page, groupChat string) (*rod.Element, error) {
	popovers, err := page.Elements(".d-popover.d-dropdown")
	if err != nil {
		return nil, err
	}

	for _, pop := range popovers {
		visible, err := pop.Visible()
		if err != nil || !visible {
			continue
		}

		options, err := pop.Elements("div.item.custom-option")
		if err != nil {
			return nil, err
		}
		for _, opt := range options {
			nameElem, err := opt.Element(".group-info .name")
			if err != nil {
				continue
			}
			name, err := nameElem.Text()
			if err != nil {
				continue
			}
			if groupChatNameMatches(name, groupChat) {
				return opt, nil
			}
		}
	}
	return nil, nil
}

func verifySelectedGroupChat(wrapper *rod.Element, groupChat string) error {
	desc, err := wrapper.Element(".d-select-description")
	if err != nil {
		return errors.New("未找到已选群聊展示区域")
	}
	text, err := desc.Text()
	if err != nil {
		return errors.Wrap(err, "读取已选群聊名称失败")
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.Contains(text, groupChatPlaceholder) {
		return errors.New("群聊选择后展示区域仍为空")
	}
	if !groupChatNameMatches(text, groupChat) {
		return errors.Errorf("群聊选择校验失败：期望 %q，实际 %q", groupChat, text)
	}
	return nil
}
