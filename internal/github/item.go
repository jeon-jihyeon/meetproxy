package github

import "strconv"

type Item struct {
	Id     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
}

func (c Client) Item(repo string, number int) (Item, error) {
	var item Item
	err := c.Get("repos/"+repo+"/issues/"+strconv.Itoa(number), &item)
	return item, err
}
