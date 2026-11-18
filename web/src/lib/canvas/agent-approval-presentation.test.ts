import assert from "node:assert/strict";
import test from "node:test";
import { agentApprovalPresentation } from "./agent-approval-presentation";

test("角色声音绑定审批保留后端提供的目标与音色说明", () => {
    const presentation = agentApprovalPresentation({
        toolName: "canvas_bind_character_voice",
        preview: {
            kind: "canvas_mutation",
            title: "确认绑定角色声音",
            description: "绑定已验收的试听音频，并创建新角色版本。",
            items: [
                {
                    operation: "bind_character_voice",
                    nodeId: "character-card",
                    nodeTitle: "角色 A",
                    nodeType: "character",
                    nodeTypeLabel: "角色卡",
                    details: ["试听音频：《试音》", "Qwen 音色：音色 A (voice-a)", "声音指令：平静地说"],
                    summary: "绑定《角色 A》的声音",
                },
            ],
        },
    });

    assert.equal(presentation.source, "server");
    assert.equal(presentation.title, "确认绑定角色声音");
    assert.equal(presentation.items.length, 1);
    assert.equal(presentation.items[0]?.operation, "bind_character_voice");
    assert.equal(presentation.items[0]?.nodeTitle, "角色 A");
    assert.deepEqual(presentation.items[0]?.details, ["试听音频：《试音》", "Qwen 音色：音色 A (voice-a)", "声音指令：平静地说"]);
});
