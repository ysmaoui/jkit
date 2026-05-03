package api

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/ysmaoui/jk/internal/jenkins"
)

func (c *Client) GetQueue() ([]jenkins.QueueItem, error) {
	resp, err := c.Get("/queue/api/json", url.Values{
		"tree": {"items[id,why,task[name,url],executable[number,url]]"},
	})
	if err != nil {
		return nil, fmt.Errorf("getting queue: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Items []jenkins.QueueItem `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding queue: %w", err)
	}
	return result.Items, nil
}

func (c *Client) CancelQueueItem(id int) error {
	path := fmt.Sprintf("/queue/cancelItem?id=%d", id)
	resp, err := c.Post(path, nil, "")
	if err != nil {
		return fmt.Errorf("cancelling queue item: %w", err)
	}
	CloseBody(resp)
	return nil
}

func (c *Client) GetQueueItem(id int) (*jenkins.QueueItem, error) {
	path := fmt.Sprintf("/queue/item/%d/api/json", id)

	resp, err := c.Get(path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting queue item: %w", err)
	}
	defer resp.Body.Close()

	var item jenkins.QueueItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return nil, fmt.Errorf("decoding queue item: %w", err)
	}
	return &item, nil
}
