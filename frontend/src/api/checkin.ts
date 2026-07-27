/**
 * Daily check-in API endpoints
 * Handles user daily check-in status and check-in action
 */

import { apiClient } from './client'

export interface CheckinStatus {
  checked_in_today: boolean
  today_date: string
  reward_amount?: number
  checkin_at?: string
  min_reward: number
  max_reward: number
}

export interface CheckinResult {
  reward_amount: number
  checkin_date: string
  checkin_at: string
  new_balance: number
}

/**
 * Get today's check-in status
 * @returns Check-in status including whether already checked in and reward range
 */
export async function getCheckinStatus(): Promise<CheckinStatus> {
  const { data } = await apiClient.get<CheckinStatus>('/checkin/status')
  return data
}

/**
 * Perform daily check-in
 * @returns Check-in result with reward amount and new balance
 */
export async function checkin(): Promise<CheckinResult> {
  const { data } = await apiClient.post<CheckinResult>('/checkin')
  return data
}

export const checkinAPI = {
  getCheckinStatus,
  checkin,
}

export default checkinAPI
