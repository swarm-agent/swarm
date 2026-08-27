import { requestJson } from '../../../app/api'

export type DesktopFeedbackCategory = 'bug' | 'general' | 'feature'

interface DesktopFeedbackSubmitResponse {
  success: boolean
  message?: string
}

export async function submitDesktopFeedback(input: {
  category: DesktopFeedbackCategory
  message: string
  formTime: number
}): Promise<DesktopFeedbackSubmitResponse> {
  return requestJson<DesktopFeedbackSubmitResponse>('/v1/feedback', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      category: input.category,
      message: input.message,
      form_time: input.formTime,
    }),
  })
}
