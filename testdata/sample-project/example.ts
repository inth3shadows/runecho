import React from 'react';
import { useState, useEffect } from 'react';

/**
 * Example TypeScript file for testing IR generation
 */

function greet(name: string): string {
    return `Hello, ${name}!`;
}

async function fetchUserData(userId: number): Promise<User> {
    const response = await fetch(`/api/users/${userId}`);
    return response.json();
}

const calculateSum = (a: number, b: number): number => {
    return a + b;
};

class UserService {
    private apiUrl: string;

    constructor(apiUrl: string) {
        this.apiUrl = apiUrl;
    }

    async getUser(id: number): Promise<User> {
        return fetchUserData(id);
    }
}

export { greet, calculateSum };
export default UserService;
