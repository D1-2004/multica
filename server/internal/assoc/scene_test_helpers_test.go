package assoc

import "github.com/google/uuid"

// sceneOf is the Host-resolved scene node of a test conversation: a stable
// scene_id derived from the conversation id, as one agent's scene directory
// would return it.
func sceneOf(conversationID string) SceneNode {
	cid := NormalizeConversationID(conversationID)
	return SceneNode{SceneID: sceneIDOf(cid), ConversationID: cid, Kind: "dm"}
}

func sceneIDOf(conversationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("scene:"+NormalizeConversationID(conversationID))).String()
}

func waitingScene(conversationID string) *SceneNode {
	node := sceneOf(conversationID)
	return &node
}
