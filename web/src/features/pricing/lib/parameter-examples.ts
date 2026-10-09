/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { PricingModel } from '../types'

export type ApiParameterExample = {
  name: string
  type:
    | 'number'
    | 'integer'
    | 'boolean'
    | 'string'
    | 'object'
    | 'array'
    | 'enum'
  defaultValue?: string | number | boolean
  range?: string
  enumValues?: string[]
  descriptionKey: string
  required?: boolean
}

const COMMON_CHAT_PARAMS: ApiParameterExample[] = [
  {
    name: 'temperature',
    type: 'number',
    defaultValue: 1,
    range: '0 ~ 2',
    descriptionKey: 'Sampling temperature; lower is more deterministic',
  },
  {
    name: 'top_p',
    type: 'number',
    defaultValue: 1,
    range: '0 ~ 1',
    descriptionKey: 'Nucleus sampling probability mass',
  },
  {
    name: 'max_tokens',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Maximum number of tokens in the response',
  },
  {
    name: 'frequency_penalty',
    type: 'number',
    defaultValue: 0,
    range: '-2 ~ 2',
    descriptionKey: 'Penalises repetition of frequent tokens',
  },
  {
    name: 'presence_penalty',
    type: 'number',
    defaultValue: 0,
    range: '-2 ~ 2',
    descriptionKey: 'Encourages introducing new topics',
  },
  {
    name: 'stop',
    type: 'array',
    descriptionKey: 'Up to 4 strings that stop generation',
  },
  {
    name: 'seed',
    type: 'integer',
    descriptionKey: 'Deterministic sampling seed (best-effort)',
  },
  {
    name: 'n',
    type: 'integer',
    defaultValue: 1,
    range: '>= 1',
    descriptionKey: 'Number of completions to generate',
  },
  {
    name: 'stream',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Stream tokens via Server-Sent Events',
  },
  {
    name: 'response_format',
    type: 'object',
    descriptionKey: 'Force JSON object or schema-conforming output',
  },
  {
    name: 'tools',
    type: 'array',
    descriptionKey: 'Tool / function declarations the model may call',
  },
  {
    name: 'tool_choice',
    type: 'string',
    enumValues: ['auto', 'none', 'required'],
    descriptionKey: 'Tool-choice policy or specific tool name',
  },
  {
    name: 'logprobs',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Return per-token log probabilities',
  },
  {
    name: 'top_logprobs',
    type: 'integer',
    range: '0 ~ 20',
    descriptionKey: 'Number of top log probabilities returned per token',
  },
  {
    name: 'logit_bias',
    type: 'object',
    descriptionKey: 'Per-token logit bias map',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const REASONING_PARAMS: ApiParameterExample[] = [
  {
    name: 'reasoning_effort',
    type: 'enum',
    enumValues: ['low', 'medium', 'high'],
    defaultValue: 'medium',
    descriptionKey: 'Controls how much the model thinks before answering',
  },
  {
    name: 'max_completion_tokens',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Maximum tokens including hidden reasoning tokens',
  },
  {
    name: 'stop',
    type: 'array',
    descriptionKey: 'Up to 4 strings that stop generation',
  },
  {
    name: 'seed',
    type: 'integer',
    descriptionKey: 'Deterministic sampling seed (best-effort)',
  },
  {
    name: 'stream',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Stream tokens via Server-Sent Events',
  },
  {
    name: 'response_format',
    type: 'object',
    descriptionKey: 'Force JSON object or schema-conforming output',
  },
  {
    name: 'tools',
    type: 'array',
    descriptionKey: 'Tool / function declarations the model may call',
  },
  {
    name: 'tool_choice',
    type: 'string',
    enumValues: ['auto', 'none', 'required'],
    descriptionKey: 'Tool-choice policy or specific tool name',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const EMBEDDING_PARAMS: ApiParameterExample[] = [
  {
    name: 'input',
    type: 'string',
    required: true,
    descriptionKey: 'Text or array of texts to embed',
  },
  {
    name: 'dimensions',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Truncate embeddings to this many dimensions',
  },
  {
    name: 'encoding_format',
    type: 'enum',
    enumValues: ['float', 'base64'],
    defaultValue: 'float',
    descriptionKey: 'Wire encoding for the embedding vectors',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const IMAGE_PARAMS: ApiParameterExample[] = [
  {
    name: 'prompt',
    type: 'string',
    required: true,
    descriptionKey: 'Text description of the desired image',
  },
  {
    name: 'size',
    type: 'enum',
    enumValues: ['256x256', '512x512', '1024x1024', '1024x1792', '1792x1024'],
    defaultValue: '1024x1024',
    descriptionKey: 'Output image size',
  },
  {
    name: 'quality',
    type: 'enum',
    enumValues: ['standard', 'hd'],
    defaultValue: 'standard',
    descriptionKey: 'Generation quality preset',
  },
  {
    name: 'style',
    type: 'enum',
    enumValues: ['vivid', 'natural'],
    defaultValue: 'vivid',
    descriptionKey: 'Aesthetic style',
  },
  {
    name: 'n',
    type: 'integer',
    defaultValue: 1,
    range: '1 ~ 10',
    descriptionKey: 'Number of images to generate',
  },
  {
    name: 'response_format',
    type: 'enum',
    enumValues: ['url', 'b64_json'],
    defaultValue: 'url',
    descriptionKey: 'How to deliver the resulting image',
  },
]

const VIDEO_PARAMS: ApiParameterExample[] = [
  {
    name: 'prompt',
    type: 'string',
    required: true,
    descriptionKey: 'Text description of the desired video',
  },
  {
    name: 'duration',
    type: 'integer',
    range: '1 ~ 60',
    descriptionKey: 'Video length in seconds',
  },
  {
    name: 'aspect_ratio',
    type: 'enum',
    enumValues: ['16:9', '9:16', '1:1'],
    defaultValue: '16:9',
    descriptionKey: 'Output aspect ratio',
  },
  {
    name: 'fps',
    type: 'integer',
    range: '8 ~ 60',
    defaultValue: 24,
    descriptionKey: 'Frames per second',
  },
]

type ApiCategory = 'reasoning' | 'embedding' | 'image' | 'video' | 'chat'

// Choose a common request example, not verified provider capability metadata.
function apiCategoryOf(model: PricingModel): ApiCategory {
  const name = model.model_name.toLowerCase()
  if (/embed|rerank/.test(name)) return 'embedding'
  if (/image|sora|veo|kling|pika|jimeng|dalle|imagen/.test(name)) {
    return /sora|veo|kling|pika|video|wan-|hunyuanvideo/.test(name)
      ? 'video'
      : 'image'
  }
  if (/whisper|tts|voice|audio/.test(name)) return 'chat'
  if (/o1|o3|o4|reasoning|thinking|deepseek-r/.test(name)) return 'reasoning'
  return 'chat'
}

/** Common parameter examples; support and defaults depend on the provider. */
export function getCommonParameterExamples(
  model: PricingModel
): ApiParameterExample[] {
  const cat = apiCategoryOf(model)
  if (cat === 'reasoning') return REASONING_PARAMS
  if (cat === 'embedding') return EMBEDDING_PARAMS
  if (cat === 'image') return IMAGE_PARAMS
  if (cat === 'video') return VIDEO_PARAMS
  return COMMON_CHAT_PARAMS
}
