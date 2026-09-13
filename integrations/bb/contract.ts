import { defineRpcContract } from '@get-bb/plugin-sdk';
import { z } from 'zod';
const feeling = z.object({
    label: z.enum(['joy', 'sadness', 'anger', 'fear', 'trust', 'disgust', 'anticipation', 'surprise', 'shame', 'guilt']),
    name: z.string().min(1).max(60).optional(), intensity: z.number().min(0).max(1),
    source: z.enum(['user', 'inferred']), cause: z.string().min(1).max(180).optional(),
}).strict();
export const memoryInput = z.object({
    items: z.array(z.object({
        summary: z.string().trim().min(1),
        kind: z.enum(['fact', 'decision', 'preference', 'project_state', 'open_loop', 'correction', 'relationship_note', 'do_not_repeat', 'emotion']),
        emotion: feeling.optional(),
        emotions: z.array(feeling).min(1).optional(),
        scope: z.enum(['project', 'personal_global']).default('project'),
        evidence: z.enum(['user_confirmed', 'current_turn', 'tool_result', 'assistant_inferred']).default('current_turn'),
        privacy: z.enum(['normal', 'sensitive', 'private']).optional(),
    }).strict()).min(1)
}).strict();
export const pulseContract = defineRpcContract({
    call: {
        input: z.object({
            workspace: z.string().min(1).max(4096), nodePath: z.string().min(1).max(4096),
            projectId: z.string().regex(/^proj_[a-z0-9]+$/),
            action: z.enum(['status', 'recall', 'remember', 'moment']), query: z.string().max(1000).default(''),
            memory: memoryInput.optional(), threadId: z.string().optional(), turnId: z.string().optional(), timestamp: z.string().optional(),
        }).strict(),
        output: z.object({
            status: z.enum(['ok', 'empty', 'unavailable', 'stored', 'pending', 'partial', 'rejected']),
            source: z.literal('Current BB project and personal memory'), mode: z.literal('project_memory'),
            elapsedMs: z.number(), context: z.string().max(3000).optional(), reason: z.string().max(120).optional(),
            fullRetrieval: z.boolean().optional(), releaseVersion: z.string().optional(), releaseEpoch: z.number().optional(),
            momentId: z.string().optional(), accepted: z.number().optional(), stored: z.number().optional(),
            moment: z.unknown().optional(), ledgerId: z.string().optional(), receipts: z.array(z.object({
                status: z.string(), id: z.string(),
                objectId: z.string().optional(), reason: z.string().optional()
            })).optional(),
        }).strict(),
    }
});
